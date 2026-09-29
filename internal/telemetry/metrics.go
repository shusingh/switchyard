package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the router's per-request Prometheus metrics. State that can
// be read on demand (backend load, index size) is exported by collectors at
// scrape time instead, so it costs nothing per request.
//
// Label sets are bounded: backend IDs and policy names come from
// configuration, status codes and rejection reasons from small fixed sets.
// Request IDs, tenants, and prompts are never labels.
type Metrics struct {
	registry *prometheus.Registry

	Requests         *prometheus.CounterVec
	TTFT             *prometheus.HistogramVec
	TPOT             *prometheus.HistogramVec
	RequestDuration  *prometheus.HistogramVec
	QueueWait        prometheus.Histogram
	Rejections       *prometheus.CounterVec
	Retries          prometheus.Counter
	PrefixMatchRatio prometheus.Histogram
	PredictionError  prometheus.Histogram
	RouteDecision    prometheus.Histogram
	Tokenize         prometheus.Histogram
	TokenizeFailures prometheus.Counter
}

// NewMetrics creates and registers the router's metrics, along with the Go
// runtime and process collectors.
func NewMetrics() *Metrics {
	latency := prometheus.ExponentialBuckets(0.005, 2, 14) // 5ms to about 41s
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "switchyard_requests_total",
			Help: "Inference requests by policy, backend, and response status code.",
		}, []string{"policy", "backend", "code"}),
		TTFT: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "switchyard_ttft_seconds",
			Help:    "Time to first generated token of streaming requests.",
			Buckets: latency,
		}, []string{"backend"}),
		TPOT: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "switchyard_tpot_seconds",
			Help:    "Mean time per output token after the first, per streaming request.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 12), // 1ms to about 2s
		}, []string{"backend"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "switchyard_request_duration_seconds",
			Help:    "End-to-end duration of inference requests, including queueing.",
			Buckets: latency,
		}, []string{"backend"}),
		QueueWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "switchyard_queue_wait_seconds",
			Help:    "Time requests waited for admission.",
			Buckets: latency,
		}),
		Rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "switchyard_admission_rejections_total",
			Help: "Requests rejected by admission control, by reason.",
		}, []string{"reason"}),
		Retries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "switchyard_retries_total",
			Help: "Requests retried on another backend after a transient failure.",
		}),
		PrefixMatchRatio: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "switchyard_prefix_match_ratio",
			Help:    "Fraction of a request's prefix blocks believed cached on the chosen backend.",
			Buckets: prometheus.LinearBuckets(0, 0.1, 11),
		}),
		PredictionError: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "switchyard_ttft_prediction_error_seconds",
			Help:    "Absolute difference between predicted and measured time to first token.",
			Buckets: latency,
		}),
		Tokenize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "switchyard_tokenize_seconds",
			Help:    "Time to tokenize a request through an engine, in precise mode.",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 14), // 100us to about 0.8s
		}),
		TokenizeFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "switchyard_tokenize_failures_total",
			Help: "Requests routed without prefix information because tokenizing failed.",
		}),
		RouteDecision: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "switchyard_route_decision_seconds",
			Help:    "Router overhead per request: parsing, keying, matching, and picking a backend.",
			Buckets: prometheus.ExponentialBuckets(0.00001, 2, 14), // 10us to about 80ms
		}),
	}
	m.registry.MustRegister(
		m.Requests, m.TTFT, m.TPOT, m.RequestDuration, m.QueueWait, m.Rejections,
		m.Retries, m.PrefixMatchRatio, m.PredictionError, m.RouteDecision,
		m.Tokenize, m.TokenizeFailures,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Register adds a collector, such as one that reports backend state at
// scrape time.
func (m *Metrics) Register(c prometheus.Collector) error { return m.registry.Register(c) }

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
