// Package config defines Switchyard's configuration: its types, defaults,
// loading from YAML, and validation.
//
// Configuration is loaded once at startup. Loading rejects unknown fields,
// applies defaults to anything left unset, and validates the result, so the
// rest of the program can trust every value it receives.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the complete router configuration.
type Config struct {
	Server   Server    `yaml:"server"`
	Backends []Backend `yaml:"backends"`
	Health   Health    `yaml:"health"`
	Proxy    Proxy     `yaml:"proxy"`
	Routing  Routing   `yaml:"routing"`
	Log      Log       `yaml:"log"`
}

// Server configures the client-facing HTTP server.
type Server struct {
	// Listen is the TCP address to listen on, for example ":8080".
	Listen string `yaml:"listen"`
	// ReadHeaderTimeout bounds how long a client may take to send headers.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	// IdleTimeout closes idle keep-alive connections.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// MaxRequestBytes limits request body size. Long agent prompts with tool
	// definitions can reach several megabytes.
	MaxRequestBytes int64 `yaml:"max_request_bytes"`
	// ShutdownTimeout bounds how long in-flight requests may run after a
	// shutdown signal before they are cancelled.
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// Backend is one OpenAI-compatible model server.
type Backend struct {
	// ID names the backend in logs, metrics, and headers. It must be unique.
	ID string `yaml:"id"`
	// URL is the base URL, without the /v1 path, for example
	// "http://localhost:8001".
	URL string `yaml:"url"`
}

// Health configures active health checking of backends.
type Health struct {
	// Path is requested with GET; any 2xx response counts as healthy.
	Path     string        `yaml:"path"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
	// UnhealthyThreshold is the number of consecutive failed checks before a
	// healthy backend is marked unhealthy.
	UnhealthyThreshold int `yaml:"unhealthy_threshold"`
	// HealthyThreshold is the number of consecutive successful checks before
	// an unhealthy backend is marked healthy.
	HealthyThreshold int `yaml:"healthy_threshold"`
}

// Proxy configures the upstream HTTP client.
type Proxy struct {
	DialTimeout time.Duration `yaml:"dial_timeout"`
	// ResponseHeaderTimeout bounds the wait for upstream response headers.
	// Non-streaming completions send headers only when generation finishes,
	// so this must exceed the longest expected generation.
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout"`
	// StreamIdleTimeout aborts a streaming response when no event arrives
	// for this long. Streams have no overall deadline; long generations are
	// legitimate, hung ones are not.
	StreamIdleTimeout   time.Duration `yaml:"stream_idle_timeout"`
	MaxIdleConnsPerHost int           `yaml:"max_idle_conns_per_host"`
}

// Routing selects the routing policy.
type Routing struct {
	// Policy names the policy, for example "round_robin". Valid names are
	// defined by the scheduler package and checked when it is constructed.
	Policy string `yaml:"policy"`
}

// Log configures logging.
type Log struct {
	// Level is one of "debug", "info", "warn", or "error".
	Level string `yaml:"level"`
}

// Load reads, decodes, defaults, and validates the configuration file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own -config flag

	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes, defaults, and validates configuration from YAML bytes.
// Unknown fields are rejected so that typos fail loudly instead of silently
// falling back to defaults.
func Parse(data []byte) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ApplyDefaults fills every unset field with its default value.
func (c *Config) ApplyDefaults() {
	setDefault(&c.Server.Listen, ":8080")
	setDefault(&c.Server.ReadHeaderTimeout, 10*time.Second)
	setDefault(&c.Server.IdleTimeout, 120*time.Second)
	setDefault(&c.Server.MaxRequestBytes, 16<<20)
	setDefault(&c.Server.ShutdownTimeout, 30*time.Second)

	setDefault(&c.Health.Path, "/health")
	setDefault(&c.Health.Interval, 2*time.Second)
	setDefault(&c.Health.Timeout, time.Second)
	setDefault(&c.Health.UnhealthyThreshold, 3)
	setDefault(&c.Health.HealthyThreshold, 2)

	setDefault(&c.Proxy.DialTimeout, 2*time.Second)
	setDefault(&c.Proxy.ResponseHeaderTimeout, 10*time.Minute)
	setDefault(&c.Proxy.StreamIdleTimeout, 60*time.Second)
	setDefault(&c.Proxy.MaxIdleConnsPerHost, 256)

	setDefault(&c.Routing.Policy, "round_robin")
	setDefault(&c.Log.Level, "info")
}

func setDefault[T comparable](field *T, value T) {
	var zero T
	if *field == zero {
		*field = value
	}
}

// Validate reports every invalid field at once, so a broken config file can
// be fixed in one pass.
func (c *Config) Validate() error {
	var errs []error
	fail := func(field, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", field, fmt.Sprintf(format, args...)))
	}

	positive := map[string]time.Duration{
		"server.read_header_timeout":    c.Server.ReadHeaderTimeout,
		"server.idle_timeout":           c.Server.IdleTimeout,
		"server.shutdown_timeout":       c.Server.ShutdownTimeout,
		"health.interval":               c.Health.Interval,
		"health.timeout":                c.Health.Timeout,
		"proxy.dial_timeout":            c.Proxy.DialTimeout,
		"proxy.response_header_timeout": c.Proxy.ResponseHeaderTimeout,
		"proxy.stream_idle_timeout":     c.Proxy.StreamIdleTimeout,
	}
	for field, d := range positive {
		if d <= 0 {
			fail(field, "must be positive, got %s", d)
		}
	}
	if c.Health.Timeout >= c.Health.Interval {
		fail("health.timeout", "must be shorter than health.interval (%s)", c.Health.Interval)
	}
	if c.Server.MaxRequestBytes <= 0 {
		fail("server.max_request_bytes", "must be positive, got %d", c.Server.MaxRequestBytes)
	}
	if c.Health.UnhealthyThreshold < 1 {
		fail("health.unhealthy_threshold", "must be at least 1, got %d", c.Health.UnhealthyThreshold)
	}
	if c.Health.HealthyThreshold < 1 {
		fail("health.healthy_threshold", "must be at least 1, got %d", c.Health.HealthyThreshold)
	}
	if c.Proxy.MaxIdleConnsPerHost < 1 {
		fail("proxy.max_idle_conns_per_host", "must be at least 1, got %d", c.Proxy.MaxIdleConnsPerHost)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		fail("log.level", "must be one of debug, info, warn, error; got %q", c.Log.Level)
	}

	if len(c.Backends) == 0 {
		fail("backends", "at least one backend is required")
	}
	seen := make(map[string]bool, len(c.Backends))
	for i, b := range c.Backends {
		field := fmt.Sprintf("backends[%d]", i)
		if b.ID == "" {
			fail(field+".id", "must not be empty")
		} else if seen[b.ID] {
			fail(field+".id", "duplicate id %q", b.ID)
		}
		seen[b.ID] = true
		if err := validateBackendURL(b.URL); err != nil {
			fail(field+".url", "%v", err)
		}
	}
	return errors.Join(errs...)
}

func validateBackendURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL %q must use http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("URL %q has no host", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("URL %q must not include a path; give the base URL without /v1", raw)
	}
	return nil
}
