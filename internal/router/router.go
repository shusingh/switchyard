// Package router assembles Switchyard from its configuration and runs it:
// health checking, the HTTP server, and graceful shutdown. The switchyard
// binary is a thin wrapper around it, which keeps the full lifecycle,
// including draining on shutdown, testable.
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/shusingh/switchyard/internal/admission"
	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/config"
	"github.com/shusingh/switchyard/internal/prefix"
	"github.com/shusingh/switchyard/internal/proxy"
	"github.com/shusingh/switchyard/internal/scheduler"
	"github.com/shusingh/switchyard/internal/server"
)

// Router is a configured Switchyard instance.
type Router struct {
	cfg     config.Config
	logger  *slog.Logger
	pool    *backend.Pool
	checker *backend.HealthChecker
	proxy   *proxy.Proxy
	policy  scheduler.Policy
	server  *server.Server
}

// New builds a Router from cfg, which must already be validated.
func New(cfg config.Config, logger *slog.Logger) (*Router, error) {
	estimator := scheduler.NewEstimator(scheduler.EstimatorConfig{
		InitialPrefillTokensPerSecond: cfg.Routing.PrefillTokensPerSecond,
		Overhead:                      cfg.Routing.TTFTOverhead,
		DecodePenalty:                 cfg.Routing.DecodePenalty,
		InitialBytesPerToken:          cfg.Routing.BytesPerToken,
		BlockBytes:                    cfg.Routing.BlockBytes,
	}, len(cfg.Backends))
	policy, err := scheduler.New(cfg.Routing.Policy, scheduler.Options{
		Estimator:  estimator,
		BalanceAbs: int64(cfg.Routing.BalanceAbs),
		BalanceRel: cfg.Routing.BalanceRel,
		TieEpsilon: cfg.Routing.TieEpsilon,
	})
	if err != nil {
		return nil, err
	}
	pool, err := backend.NewPool(cfg.Backends, backend.BreakerConfig{
		FailureThreshold: cfg.Proxy.BreakerFailures,
		Cooldown:         cfg.Proxy.BreakerCooldown,
	})
	if err != nil {
		return nil, err
	}
	admit, err := newAdmission(cfg.Admission)
	if err != nil {
		return nil, err
	}
	capacities := make([]int, len(cfg.Backends))
	for i := range capacities {
		capacities[i] = cfg.IndexCapacityBlocks(i)
	}
	px := proxy.New(cfg.Proxy)

	return &Router{
		cfg:     cfg,
		logger:  logger,
		pool:    pool,
		checker: backend.NewHealthChecker(pool, cfg.Health, logger),
		proxy:   px,
		policy:  policy,
		server: server.New(server.Options{
			Pool:              pool,
			Policy:            policy,
			Proxy:             px,
			Keyer:             prefix.NewKeyer(cfg.Routing.BlockBytes, cfg.Routing.MaxBlocks),
			Index:             prefix.NewIndex(capacities, cfg.Routing.IndexTTL, nil),
			Estimator:         estimator,
			Logger:            logger,
			MaxRequestBytes:   cfg.Server.MaxRequestBytes,
			ExplainHeaders:    cfg.Server.ExplainHeaders,
			Admission:         admit,
			TrustTenantHeader: cfg.Admission.TrustTenantHeader,
			MaxRetries:        cfg.Proxy.MaxRetries,
			RetryBudgetRatio:  cfg.Proxy.RetryBudgetRatio,
		}),
	}, nil
}

func newAdmission(cfg config.Admission) (*admission.Controller, error) {
	tenant := func(t config.Tenant) admission.TenantConfig {
		return admission.TenantConfig{
			Name:            t.Name,
			APIKeys:         t.APIKeys,
			Weight:          t.Weight,
			TokensPerSecond: t.TokensPerSecond,
			Burst:           t.Burst,
			MaxQueued:       t.MaxQueued,
		}
	}
	ac := admission.Config{
		MaxInFlight:  cfg.MaxInFlight,
		QueueTimeout: cfg.QueueTimeout,
		Quantum:      cfg.QuantumTokens,
		Default:      tenant(cfg.DefaultTenant),
	}
	for _, t := range cfg.Tenants {
		ac.Tenants = append(ac.Tenants, tenant(t))
	}
	return admission.New(ac, nil)
}

// Serve accepts connections on ln until ctx is cancelled, then stops
// accepting new requests and lets in-flight ones finish for up to the
// configured shutdown timeout before closing what remains. Health checks keep
// running during the drain.
func (r *Router) Serve(ctx context.Context, ln net.Listener) error {
	healthCtx, stopHealth := context.WithCancel(context.WithoutCancel(ctx))
	var wg sync.WaitGroup
	defer func() {
		stopHealth()
		wg.Wait()
		r.checker.CloseIdleConnections()
		r.proxy.CloseIdleConnections()
	}()
	r.checker.CheckAll(ctx) // serve immediately if backends are already up
	wg.Go(func() { r.checker.Run(healthCtx) })

	httpServer := &http.Server{
		Handler:           r.server.Handler(),
		ReadHeaderTimeout: r.cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       r.cfg.Server.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(r.logger.Handler(), slog.LevelWarn),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(ln) }()
	r.logger.Info("switchyard started",
		slog.String("listen", ln.Addr().String()),
		slog.String("policy", r.policy.Name()),
		slog.Int("backends", len(r.cfg.Backends)))

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	r.logger.Info("shutting down", slog.Duration("drain_timeout", r.cfg.Server.ShutdownTimeout))
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(drainCtx); err != nil {
		// Requests still running after the drain timeout are cut off.
		r.logger.Warn("drain timeout exceeded; closing remaining connections", slog.String("error", err.Error()))
		_ = httpServer.Close()
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	r.logger.Info("switchyard stopped")
	return nil
}
