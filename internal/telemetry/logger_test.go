package telemetry

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNewLogger(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger, err := NewLogger(&buf, "warn")
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	logger.Info("dropped")
	logger.Warn("kept")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log output %q is not one JSON object: %v", buf.String(), err)
	}
	if entry["msg"] != "kept" || entry["level"] != "WARN" {
		t.Errorf("log entry = %v, want msg=kept level=WARN", entry)
	}
}

func TestNewLoggerRejectsUnknownLevel(t *testing.T) {
	t.Parallel()
	if _, err := NewLogger(&bytes.Buffer{}, "verbose"); err == nil {
		t.Error(`NewLogger("verbose") error = nil, want an error`)
	}
}
