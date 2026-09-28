package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimalYAML = `
backends:
  - id: a
    url: http://localhost:8001
`

func TestParseAppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(minimalYAML))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"server.listen", cfg.Server.Listen, ":8080"},
		{"server.max_request_bytes", cfg.Server.MaxRequestBytes, int64(16 << 20)},
		{"health.path", cfg.Health.Path, "/health"},
		{"health.interval", cfg.Health.Interval, 2 * time.Second},
		{"proxy.stream_idle_timeout", cfg.Proxy.StreamIdleTimeout, 60 * time.Second},
		{"routing.policy", cfg.Routing.Policy, "round_robin"},
		{"log.level", cfg.Log.Level, "info"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseKeepsExplicitValues(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(`
server:
  listen: ":9000"
health:
  interval: 500ms
  timeout: 100ms
routing:
  policy: random
backends:
  - id: a
    url: http://localhost:8001
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Server.Listen != ":9000" {
		t.Errorf("server.listen = %q, want %q", cfg.Server.Listen, ":9000")
	}
	if cfg.Health.Interval != 500*time.Millisecond {
		t.Errorf("health.interval = %v, want 500ms", cfg.Health.Interval)
	}
	if cfg.Routing.Policy != "random" {
		t.Errorf("routing.policy = %q, want %q", cfg.Routing.Policy, "random")
	}
}

func TestParseRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		wantErr string // substring naming the offending field
	}{
		{
			name:    "unknown field",
			yaml:    minimalYAML + "servr:\n  listen: x\n",
			wantErr: "servr",
		},
		{
			name:    "no backends",
			yaml:    "server:\n  listen: \":8080\"\n",
			wantErr: "backends: at least one backend",
		},
		{
			name:    "duplicate backend id",
			yaml:    minimalYAML + "  - id: a\n    url: http://localhost:8002\n",
			wantErr: `backends[1].id: duplicate id "a"`,
		},
		{
			name:    "backend url with path",
			yaml:    "backends:\n  - id: a\n    url: http://localhost:8001/v1\n",
			wantErr: "backends[0].url",
		},
		{
			name:    "backend url without scheme",
			yaml:    "backends:\n  - id: a\n    url: localhost:8001\n",
			wantErr: "backends[0].url",
		},
		{
			name:    "negative duration",
			yaml:    minimalYAML + "proxy:\n  dial_timeout: -1s\n",
			wantErr: "proxy.dial_timeout: must be positive",
		},
		{
			name:    "health timeout not shorter than interval",
			yaml:    minimalYAML + "health:\n  interval: 1s\n  timeout: 1s\n",
			wantErr: "health.timeout",
		},
		{
			name:    "bad log level",
			yaml:    minimalYAML + "log:\n  level: verbose\n",
			wantErr: "log.level",
		},
		{
			name:    "malformed duration",
			yaml:    minimalYAML + "health:\n  interval: soon\n",
			wantErr: "parse config",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("Parse() error = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("backends:\n  - id: \"\"\n    url: ftp://x\nlog:\n  level: loud\n"))
	if err == nil {
		t.Fatal("Parse() error = nil, want errors")
	}
	for _, field := range []string{"backends[0].id", "backends[0].url", "log.level"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not mention %s", err, field)
		}
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "switchyard.yaml")
	if err := os.WriteFile(path, []byte(minimalYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].ID != "a" {
		t.Errorf("Load() backends = %+v, want one backend with id a", cfg.Backends)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("Load(missing file) error = nil, want an error")
	}
}
