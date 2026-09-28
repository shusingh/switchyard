// Package telemetry builds the observability plumbing shared by Switchyard's
// binaries.
package telemetry

import (
	"fmt"
	"io"
	"log/slog"
)

// NewLogger returns a JSON logger that writes to w at the named level
// ("debug", "info", "warn", or "error").
func NewLogger(w io.Writer, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log level %q: %w", level, err)
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}
