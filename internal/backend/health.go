package backend

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/shusingh/switchyard/internal/config"
)

// HealthChecker actively probes every backend and updates its health.
//
// A backend flips to unhealthy after UnhealthyThreshold consecutive failures
// and back to healthy after HealthyThreshold consecutive successes. The very
// first successful check marks a backend healthy immediately, so the router
// can serve traffic as soon as it starts.
type HealthChecker struct {
	pool   *Pool
	cfg    config.Health
	client *http.Client
	logger *slog.Logger
}

// NewHealthChecker returns a checker for pool. It uses its own HTTP client so
// probe traffic never competes with proxied requests for connections.
func NewHealthChecker(pool *Pool, cfg config.Health, logger *slog.Logger) *HealthChecker {
	transport := &http.Transport{
		Proxy:               nil,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     2 * cfg.Interval,
	}
	return &HealthChecker{
		pool:   pool,
		cfg:    cfg,
		client: &http.Client{Transport: transport, Timeout: cfg.Timeout},
		logger: logger,
	}
}

// CloseIdleConnections closes idle probe connections. It is called on
// shutdown so no connection outlives the checker.
func (h *HealthChecker) CloseIdleConnections() { h.client.CloseIdleConnections() }

// Run checks every backend once per interval until ctx is cancelled.
func (h *HealthChecker) Run(ctx context.Context) {
	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.CheckAll(ctx)
		}
	}
}

// CheckAll probes every backend concurrently and returns when all probes
// have finished.
func (h *HealthChecker) CheckAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, b := range h.pool.Backends() {
		wg.Go(func() { h.record(b, h.probe(ctx, b)) })
	}
	wg.Wait()
}

// probe reports whether one health request succeeded.
func (h *HealthChecker) probe(ctx context.Context, b *Backend) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.Endpoint(h.cfg.Path, "").String(), http.NoBody)
	if err != nil {
		return false
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused for the next probe.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// record applies one probe result to b's streaks and health.
func (h *HealthChecker) record(b *Backend, ok bool) {
	firstCheck := !b.checked
	b.checked = true
	if ok {
		b.failStreak = 0
		b.okStreak++
		if (firstCheck || b.okStreak >= h.cfg.HealthyThreshold) && b.setHealthy(true) {
			h.logger.Info("backend healthy",
				slog.String("backend", b.ID()),
				slog.Uint64("generation", b.Generation()))
		}
		return
	}
	b.okStreak = 0
	b.failStreak++
	if b.failStreak >= h.cfg.UnhealthyThreshold && b.setHealthy(false) {
		h.logger.Warn("backend unhealthy",
			slog.String("backend", b.ID()),
			slog.Int("consecutive_failures", b.failStreak))
	}
}
