// Command switchyard runs the prefix-cache-aware router in front of a pool of
// OpenAI-compatible model servers.
//
// Usage:
//
//	switchyard -config switchyard.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/shusingh/switchyard/internal/config"
	"github.com/shusingh/switchyard/internal/router"
	"github.com/shusingh/switchyard/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "switchyard:", err)
		os.Exit(1)
	}
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
	r, err := router.New(cfg, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Server.Listen)
	if err != nil {
		return err
	}
	return r.Serve(ctx, ln)
}
