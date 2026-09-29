// Command simengine runs a simulated OpenAI-compatible model server for
// testing and experimenting with Switchyard without a GPU.
//
// Usage:
//
//	simengine -listen :9001 -capacity-blocks 2340 -prefill-rate 14000
//
// With -replicas N it serves N engines on consecutive ports starting at the
// -listen port. Adding -shared-device puts them on one simulated accelerator
// that runs one engine step at a time, like replicas time-slicing one GPU.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	bytesPerToken := flag.Float64("bytes-per-token", d.BytesPerToken, "prompt bytes per simulated token")
	capacity := flag.Int("capacity-blocks", d.CapacityBlocks, "KV cache capacity in blocks")
	prefillRate := flag.Float64("prefill-rate", d.PrefillTokensPerSecond, "prompt tokens processed per second")
	step := flag.Duration("step", d.StepOverhead, "fixed duration of one engine step")
	decodeCost := flag.Duration("decode-cost", d.DecodeCostPerSequence, "added step time per decoding sequence")
	batchTokens := flag.Int("max-batch-tokens", d.MaxBatchTokens, "prefill token budget per step")
	maxRunning := flag.Int("max-running", d.MaxRunning, "maximum concurrently running sequences")
	maxModelLen := flag.Int("max-model-len", d.MaxModelLen, "maximum prompt plus output tokens")
	replicas := flag.Int("replicas", 1, "engines to serve on consecutive ports")
	sharedDevice := flag.Bool("shared-device", false, "run all replicas on one simulated accelerator")
	flag.Parse()

	if *replicas < 1 {
		return fmt.Errorf("-replicas must be at least 1, got %d", *replicas)
	}
	host, portText, err := net.SplitHostPort(*listen)
	if err != nil {
		return fmt.Errorf("-listen: %w", err)
	}
	basePort, err := strconv.Atoi(portText)
	if err != nil {
		return fmt.Errorf("-listen port: %w", err)
	}

	logger, err := telemetry.NewLogger(os.Stdout, "info")
	if err != nil {
		return err
	}
	opts := sim.Options{
		Model: *model,
		Cost: sim.CostModel{
			BlockTokens:            *blockTokens,
			BytesPerToken:          *bytesPerToken,
			CapacityBlocks:         *capacity,
			PrefillTokensPerSecond: *prefillRate,
			StepOverhead:           *step,
			DecodeCostPerSequence:  *decodeCost,
			MaxBatchTokens:         *batchTokens,
			MaxRunning:             *maxRunning,
			MaxModelLen:            *maxModelLen,
		},
		DefaultOutputTokens: *outputTokens,
	}
	if *sharedDevice {
		opts.Device = sim.NewDevice()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	engineCtx, stopEngines := context.WithCancel(context.Background())
	defer func() {
		stopEngines()
		wg.Wait()
	}()

	servers := make([]*http.Server, *replicas)
	serveErr := make(chan error, *replicas)
	for i := range servers {
		engine := sim.NewEngine(opts)
		wg.Go(func() { engine.Run(engineCtx) })
		addr := net.JoinHostPort(host, strconv.Itoa(basePort+i))
		servers[i] = &http.Server{
			Addr:              addr,
			Handler:           engine.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		}
		go func() { serveErr <- servers[i].ListenAndServe() }()
		logger.Info("simengine started",
			slog.String("listen", addr),
			slog.String("model", *model),
			slog.Int("capacity_blocks", *capacity),
			slog.Bool("shared_device", *sharedDevice))
	}

	select {
	case err := <-serveErr:
		shutdown(servers)
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdown(servers)
	for range servers {
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
	}
	return nil
}

// shutdown stops every server, waiting up to ten seconds for in-flight
// requests.
func shutdown(servers []*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
}
