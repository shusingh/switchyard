// Package simtest starts simulated engines for tests, in the style of
// net/http/httptest.
package simtest

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shusingh/switchyard/internal/sim"
)

// FastCost is a cost model with near-zero latency, for tests that exercise
// behavior rather than timing.
func FastCost() sim.CostModel {
	return sim.CostModel{
		PrefillTokensPerSecond: 1e9,
		StepOverhead:           time.Millisecond,
		DecodeCostPerSequence:  time.Microsecond,
	}
}

// Server is a running simulated engine behind an HTTP test server.
type Server struct {
	*httptest.Server
	Engine *sim.Engine
}

// Start runs an engine with opts and serves it over HTTP. The engine and
// server stop when the test ends.
func Start(t testing.TB, opts sim.Options) *Server {
	t.Helper()
	engine := sim.NewEngine(opts)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		engine.Run(ctx)
		close(stopped)
	}()
	srv := httptest.NewServer(engine.Handler())
	// Cleanups run in reverse order: close the server first so no request
	// arrives after the engine stops, then stop the engine.
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	t.Cleanup(srv.Close)
	return &Server{Server: srv, Engine: engine}
}
