// Command simengine runs a simulated OpenAI-compatible model server for
// testing and experimenting with Switchyard without a GPU.
//
// Usage:
//
//	simengine -listen :9001 -capacity-blocks 2340 -prefill-rate 14000
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
	d := sim.DefaultCostModel()
	listen := flag.String("listen", ":9001", "listen address")
	model := flag.String("model", "sim-model", "model name to serve")
	outputTokens := flag.Int("output-tokens", 16, "tokens generated when a request sets no limit")
	blockTokens := flag.Int("block-tokens", d.BlockTokens, "tokens per KV cache block")
	capacity := flag.Int("capacity-blocks", d.CapacityBlocks, "KV cache capacity in blocks")
	prefillRate := flag.Float64("prefill-rate", d.PrefillTokensPerSecond, "prompt tokens processed per second")
	step := flag.Duration("step", d.StepOverhead, "fixed duration of one engine step")
	decodeCost := flag.Duration("decode-cost", d.DecodeCostPerSequence, "added step time per decoding sequence")
	batchTokens := flag.Int("max-batch-tokens", d.MaxBatchTokens, "prefill token budget per step")
	maxRunning := flag.Int("max-running", d.MaxRunning, "maximum concurrently running sequences")
	maxModelLen := flag.Int("max-model-len", d.MaxModelLen, "maximum prompt plus output tokens")
	flag.Parse()

	logger, err := telemetry.NewLogger(os.Stdout, "info")
	if err != nil {
		return err
	}
	engine := sim.NewEngine(sim.Options{
		Model: *model,
		Cost: sim.CostModel{
			BlockTokens:            *blockTokens,
			CapacityBlocks:         *capacity,
			PrefillTokensPerSecond: *prefillRate,
			StepOverhead:           *step,
			DecodeCostPerSequence:  *decodeCost,
			MaxBatchTokens:         *batchTokens,
			MaxRunning:             *maxRunning,
			MaxModelLen:            *maxModelLen,
		},
		DefaultOutputTokens: *outputTokens,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	engineCtx, stopEngine := context.WithCancel(context.Background())
	defer func() {
		stopEngine()
		wg.Wait()
	}()
	wg.Go(func() { engine.Run(engineCtx) })

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           engine.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()
	logger.Info("simengine started",
		slog.String("listen", *listen),
		slog.String("model", *model),
		slog.Int("capacity_blocks", *capacity))

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
