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
	Server    Server    `yaml:"server"`
	Backends  []Backend `yaml:"backends"`
	Health    Health    `yaml:"health"`
	Proxy     Proxy     `yaml:"proxy"`
	Routing   Routing   `yaml:"routing"`
	Admission Admission `yaml:"admission"`
	Log       Log       `yaml:"log"`
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
	// ExplainHeaders adds X-Switchyard-* response headers describing each
	// routing decision. They reveal internal topology, so they are off by
	// default; benchmarks turn them on to measure prediction error.
	ExplainHeaders bool `yaml:"explain_headers"`
}

// Backend is one OpenAI-compatible model server.
type Backend struct {
	// ID names the backend in logs, metrics, and headers. It must be unique.
	ID string `yaml:"id"`
	// URL is the base URL, without the /v1 path, for example
	// "http://localhost:8001".
	URL string `yaml:"url"`
	// KVCapacityTokens overrides routing.kv_capacity_tokens for this backend.
	KVCapacityTokens int `yaml:"kv_capacity_tokens"`
	// APIKey, if set, is sent to the backend as a bearer token. Client
	// credentials are never forwarded to backends.
	APIKey string `yaml:"api_key"`
	// KVEventsEndpoint is the backend's KV cache event publisher, for
	// example "tcp://localhost:5601". Required in precise mode.
	KVEventsEndpoint string `yaml:"kv_events_endpoint"`
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
	// MaxRetries is how many times a request that failed transiently
	// (connection failure or 502, 503, 504) before anything reached the
	// client may be retried on another backend.
	MaxRetries int `yaml:"max_retries"`
	// RetryBudgetRatio caps retries as a fraction of requests, so failures
	// do not multiply the load on the backends that remain.
	RetryBudgetRatio float64 `yaml:"retry_budget_ratio"`
	// BreakerFailures is the number of consecutive failed requests that
	// takes a backend out of rotation; 0 disables circuit breaking.
	BreakerFailures int `yaml:"breaker_failures"`
	// BreakerCooldown is how long a backend stays out of rotation before a
	// single trial request is let through.
	BreakerCooldown time.Duration `yaml:"breaker_cooldown"`
}

// Routing selects the routing policy and configures prefix tracking. See
// docs/engineering/design.md sections 6, 7, and 9.
type Routing struct {
	// Policy names the policy, for example "round_robin". Valid names are
	// defined by the scheduler package and checked when it is constructed.
	Policy string `yaml:"policy"`
	// BlockBytes is the size of one hashed prefix block of the canonical
	// request.
	BlockBytes int `yaml:"block_bytes"`
	// MaxBlocks caps how many blocks of one request are keyed, bounding the
	// work per request.
	MaxBlocks int `yaml:"max_blocks"`
	// IndexTTL is how long the router trusts that a backend still caches a
	// prefix it was sent.
	IndexTTL time.Duration `yaml:"index_ttl"`
	// BytesPerToken converts request bytes to estimated tokens.
	BytesPerToken float64 `yaml:"bytes_per_token"`
	// KVCapacityTokens is a backend's KV cache capacity in tokens, which
	// bounds how many prefix blocks the router believes it holds. Backends
	// may override it.
	KVCapacityTokens int `yaml:"kv_capacity_tokens"`

	// PrefillTokensPerSecond is each backend's assumed prefill throughput
	// until observed time to first token refines it.
	PrefillTokensPerSecond float64 `yaml:"prefill_tokens_per_second"`
	// TTFTOverhead is the fixed part of time to first token.
	TTFTOverhead time.Duration `yaml:"ttft_overhead"`
	// DecodePenalty is added to a prediction per request the backend is
	// decoding.
	DecodePenalty time.Duration `yaml:"decode_penalty"`
	// BalanceAbs and BalanceRel define skewed load for estimated_ttft's
	// imbalance guard: the busiest backend has more than BalanceAbs more
	// in-flight requests than the idlest, and more than BalanceRel times as
	// many.
	BalanceAbs int     `yaml:"balance_abs"`
	BalanceRel float64 `yaml:"balance_rel"`
	// TieEpsilon treats predictions within this fraction of the best as
	// ties, chosen at random.
	TieEpsilon float64 `yaml:"tie_epsilon"`

	// PrefixMode is "approximate" (the default: hash request bytes, track
	// what was routed where) or "precise" (tokenize through the engine, hash
	// as vLLM does, and track the engines' KV cache events). See
	// design.md section 7.4.
	PrefixMode string `yaml:"prefix_mode"`
	// EngineBlockTokens is the engines' KV block size in tokens (vLLM's
	// --block-size). Used in precise mode.
	EngineBlockTokens int `yaml:"engine_block_tokens"`
	// KVEventsTopic is the topic the engines publish KV events on.
	KVEventsTopic string `yaml:"kv_events_topic"`
}

// Prefix modes.
const (
	PrefixModeApproximate = "approximate"
	PrefixModePrecise     = "precise"
)

// IndexCapacityBlocks returns backend b's prefix index budget in blocks: its
// KV capacity converted from tokens to canonical request bytes.
func (c *Config) IndexCapacityBlocks(b int) int {
	tokens := c.Routing.KVCapacityTokens
	if override := c.Backends[b].KVCapacityTokens; override > 0 {
		tokens = override
	}
	if c.Routing.PrefixMode == PrefixModePrecise {
		// Precise keys are the engine's own blocks.
		return max(1, tokens/c.Routing.EngineBlockTokens)
	}
	return max(1, int(float64(tokens)*c.Routing.BytesPerToken)/c.Routing.BlockBytes)
}

// Admission configures tenant budgets, the concurrency cap, and queuing. See
// docs/engineering/design.md section 10.
type Admission struct {
	// MaxInFlight caps concurrently admitted requests across all tenants.
	MaxInFlight int `yaml:"max_in_flight"`
	// QueueTimeout bounds how long a request waits for capacity.
	QueueTimeout time.Duration `yaml:"queue_timeout"`
	// QuantumTokens is the fair-queuing credit per round, times a tenant's
	// weight; set it near a typical request's cost in tokens.
	QuantumTokens float64 `yaml:"quantum_tokens"`
	// TrustTenantHeader identifies tenants by the X-Tenant-ID header. Enable
	// it only behind a gateway that sets the header itself.
	TrustTenantHeader bool `yaml:"trust_tenant_header"`
	// DefaultTenant applies to requests that match no tenant.
	DefaultTenant Tenant   `yaml:"default_tenant"`
	Tenants       []Tenant `yaml:"tenants"`
}

// Tenant describes one client's share of capacity.
type Tenant struct {
	Name string `yaml:"name"`
	// APIKeys identify the tenant from the request's bearer token.
	APIKeys []string `yaml:"api_keys"`
	// Weight is the tenant's share of capacity under contention.
	Weight int `yaml:"weight"`
	// TokensPerSecond is the tenant's sustained budget in estimated tokens;
	// zero means unlimited. Burst is how far it may run ahead, defaulting to
	// ten seconds of budget.
	TokensPerSecond float64 `yaml:"tokens_per_second"`
	Burst           float64 `yaml:"burst"`
	// MaxQueued bounds the tenant's waiting requests.
	MaxQueued int `yaml:"max_queued"`
}

func (t *Tenant) applyDefaults() {
	setDefault(&t.Weight, 1)
	setDefault(&t.MaxQueued, 256)
	if t.TokensPerSecond > 0 {
		setDefault(&t.Burst, 10*t.TokensPerSecond)
	}
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
	setDefault(&c.Proxy.MaxRetries, 1)
	setDefault(&c.Proxy.RetryBudgetRatio, 0.1)
	setDefault(&c.Proxy.BreakerFailures, 5)
	setDefault(&c.Proxy.BreakerCooldown, 30*time.Second)

	setDefault(&c.Routing.Policy, "prefix_affinity")
	setDefault(&c.Routing.BlockBytes, 128)
	setDefault(&c.Routing.MaxBlocks, 4096)
	setDefault(&c.Routing.IndexTTL, 10*time.Minute)
	setDefault(&c.Routing.BytesPerToken, 4.0)
	setDefault(&c.Routing.KVCapacityTokens, 100_000)
	setDefault(&c.Routing.PrefillTokensPerSecond, 10_000)
	setDefault(&c.Routing.TTFTOverhead, 20*time.Millisecond)
	setDefault(&c.Routing.DecodePenalty, 500*time.Microsecond)
	setDefault(&c.Routing.BalanceAbs, 16)
	setDefault(&c.Routing.BalanceRel, 1.5)
	setDefault(&c.Routing.TieEpsilon, 0.05)
	setDefault(&c.Routing.PrefixMode, PrefixModeApproximate)
	setDefault(&c.Routing.EngineBlockTokens, 16)
	setDefault(&c.Routing.KVEventsTopic, "kv-events")
	setDefault(&c.Admission.MaxInFlight, 512)
	setDefault(&c.Admission.QueueTimeout, 30*time.Second)
	setDefault(&c.Admission.QuantumTokens, 4096)
	setDefault(&c.Admission.DefaultTenant.Name, "default")
	c.Admission.DefaultTenant.applyDefaults()
	for i := range c.Admission.Tenants {
		c.Admission.Tenants[i].applyDefaults()
	}

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
		"routing.index_ttl":             c.Routing.IndexTTL,
		"admission.queue_timeout":       c.Admission.QueueTimeout,
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
	if c.Routing.BlockBytes < 8 {
		fail("routing.block_bytes", "must be at least 8, got %d", c.Routing.BlockBytes)
	}
	if c.Routing.MaxBlocks < 1 {
		fail("routing.max_blocks", "must be at least 1, got %d", c.Routing.MaxBlocks)
	}
	if c.Routing.BytesPerToken <= 0 {
		fail("routing.bytes_per_token", "must be positive, got %g", c.Routing.BytesPerToken)
	}
	if c.Routing.PrefillTokensPerSecond <= 0 {
		fail("routing.prefill_tokens_per_second", "must be positive, got %g", c.Routing.PrefillTokensPerSecond)
	}
	if c.Routing.TTFTOverhead < 0 || c.Routing.DecodePenalty < 0 {
		fail("routing.ttft_overhead", "ttft_overhead and decode_penalty must not be negative")
	}
	if c.Routing.BalanceAbs < 1 || c.Routing.BalanceRel < 1 {
		fail("routing.balance_abs", "balance_abs must be at least 1 and balance_rel at least 1.0")
	}
	if c.Routing.TieEpsilon < 0 || c.Routing.TieEpsilon > 1 {
		fail("routing.tie_epsilon", "must be between 0 and 1, got %g", c.Routing.TieEpsilon)
	}
	switch c.Routing.PrefixMode {
	case PrefixModeApproximate:
	case PrefixModePrecise:
		for i, b := range c.Backends {
			if b.KVEventsEndpoint == "" {
				fail(fmt.Sprintf("backends[%d].kv_events_endpoint", i), "required when routing.prefix_mode is precise")
			}
		}
	default:
		fail("routing.prefix_mode", "must be approximate or precise, got %q", c.Routing.PrefixMode)
	}
	if c.Routing.EngineBlockTokens < 1 {
		fail("routing.engine_block_tokens", "must be at least 1, got %d", c.Routing.EngineBlockTokens)
	}
	if c.Routing.KVCapacityTokens < 1 {
		fail("routing.kv_capacity_tokens", "must be at least 1, got %d", c.Routing.KVCapacityTokens)
	}
	if c.Proxy.MaxRetries < 0 || c.Proxy.RetryBudgetRatio < 0 || c.Proxy.BreakerFailures < 0 || c.Proxy.BreakerCooldown < 0 {
		fail("proxy", "max_retries, retry_budget_ratio, breaker_failures, and breaker_cooldown must not be negative")
	}
	if c.Admission.MaxInFlight < 1 {
		fail("admission.max_in_flight", "must be at least 1, got %d", c.Admission.MaxInFlight)
	}
	if c.Admission.QuantumTokens <= 0 {
		fail("admission.quantum_tokens", "must be positive, got %g", c.Admission.QuantumTokens)
	}
	tenants := map[string]bool{c.Admission.DefaultTenant.Name: true}
	for i, t := range c.Admission.Tenants {
		field := fmt.Sprintf("admission.tenants[%d]", i)
		switch {
		case t.Name == "":
			fail(field+".name", "must not be empty")
		case tenants[t.Name]:
			fail(field+".name", "duplicate tenant %q", t.Name)
		}
		tenants[t.Name] = true
		if t.Weight < 1 || t.MaxQueued < 0 || t.TokensPerSecond < 0 || t.Burst < 0 {
			fail(field, "weight must be at least 1 and budgets must not be negative")
		}
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
		if b.KVCapacityTokens < 0 {
			fail(field+".kv_capacity_tokens", "must not be negative, got %d", b.KVCapacityTokens)
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
