// Command simengine runs a simulated OpenAI-compatible model server for
// testing Switchyard without a GPU.
//
// Usage:
//
//	simengine -listen :9001 -first-token-delay 50ms -token-interval 10ms
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
	"syscall"
	"time"

	"github.com/shusingh/switchyard/internal/sim"
	"github.com/shusingh/switchyard/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "simengine:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", ":9001", "listen address")
	model := flag.String("model", "sim-model", "model name to serve")
	firstToken := flag.Duration("first-token-delay", 50*time.Millisecond, "time to the first generated token")
	interval := flag.Duration("token-interval", 10*time.Millisecond, "time between generated tokens")
	outputTokens := flag.Int("output-tokens", 16, "tokens generated when a request sets no limit")
	flag.Parse()

	logger, err := telemetry.NewLogger(os.Stdout, "info")
	if err != nil {
		return err
	}
	engine := sim.NewEngine(sim.Options{
		Model:               *model,
		FirstTokenDelay:     *firstToken,
		TokenInterval:       *interval,
		DefaultOutputTokens: *outputTokens,
	})
	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           engine.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()
	logger.Info("simengine started", slog.String("listen", *listen), slog.String("model", *model))

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		_ = httpServer.Close()
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
