// Command switchyard runs the prefix-cache-aware router in front of a pool of
// OpenAI-compatible model servers.
//
// Usage:
//
//	switchyard -config switchyard.yaml
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/shusingh/switchyard/internal/admission"
	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/config"
	"github.com/shusingh/switchyard/internal/prefix"
	"github.com/shusingh/switchyard/internal/proxy"
	"github.com/shusingh/switchyard/internal/scheduler"
	"github.com/shusingh/switchyard/internal/server"
	"github.com/shusingh/switchyard/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "switchyard:", err)
		os.Exit(1)
	}
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

func run() error {
	configPath := flag.String("config", "switchyard.yaml", "path to the YAML configuration file")
	listen := flag.String("listen", "", "listen address; overrides server.listen in the config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Server.Listen = *listen
	}
	logger, err := telemetry.NewLogger(os.Stdout, cfg.Log.Level)
	if err != nil {
		return err
	}
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
		return err
	}
	pool, err := backend.NewPool(cfg.Backends, backend.BreakerConfig{
		FailureThreshold: cfg.Proxy.BreakerFailures,
		Cooldown:         cfg.Proxy.BreakerCooldown,
	})
	if err != nil {
		return err
	}
	admit, err := newAdmission(cfg.Admission)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Health checks keep running while in-flight requests drain after a
	// shutdown signal, so they get their own lifetime.
	healthCtx, stopHealth := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		stopHealth()
		wg.Wait()
	}()
	checker := backend.NewHealthChecker(pool, cfg.Health, logger)
	checker.CheckAll(ctx) // serve immediately if backends are already up
	wg.Go(func() { checker.Run(healthCtx) })

	capacities := make([]int, len(cfg.Backends))
	for i := range capacities {
		capacities[i] = cfg.IndexCapacityBlocks(i)
	}
	srv := server.New(server.Options{
		Pool:              pool,
		Policy:            policy,
		Proxy:             proxy.New(cfg.Proxy),
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
	})
	httpServer := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()
	logger.Info("switchyard started",
		slog.String("listen", cfg.Server.Listen),
		slog.String("policy", policy.Name()),
		slog.Int("backends", len(cfg.Backends)))

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down", slog.Duration("drain_timeout", cfg.Server.ShutdownTimeout))
	drainCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(drainCtx); err != nil {
		// Requests still running after the drain timeout are cut off.
		logger.Warn("drain timeout exceeded; closing remaining connections", slog.String("error", err.Error()))
		_ = httpServer.Close()
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("switchyard stopped")
	return nil
}
