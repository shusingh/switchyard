package router

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/shusingh/switchyard/internal/admission"
	"github.com/shusingh/switchyard/internal/backend"
	"github.com/shusingh/switchyard/internal/prefix"
	"github.com/shusingh/switchyard/internal/scheduler"
)

// stateCollector exports the router's live state when Prometheus scrapes:
// backend load and health, learned estimates, index size, and admission
// queues. Reading state at scrape time keeps it off the request path.
type stateCollector struct {
	pool      *backend.Pool
	estimator *scheduler.Estimator
	index     *prefix.Index
	admission *admission.Controller

	inFlight, pendingPrefill, decoding, healthy, available, prefillRate *prometheus.Desc
	bytesPerToken, indexEntries, admitted, queued                       *prometheus.Desc
}

func newStateCollector(pool *backend.Pool, est *scheduler.Estimator, ix *prefix.Index, ac *admission.Controller) *stateCollector {
	perBackend := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, []string{"backend"}, nil)
	}
	global := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, nil, nil)
	}
	return &stateCollector{
		pool: pool, estimator: est, index: ix, admission: ac,
		inFlight:       perBackend("switchyard_backend_in_flight", "Requests being served by the backend."),
		pendingPrefill: perBackend("switchyard_backend_pending_prefill_tokens", "Estimated uncached prompt tokens of requests not yet at their first token."),
		decoding:       perBackend("switchyard_backend_decoding", "Requests generating output on the backend."),
		healthy:        perBackend("switchyard_backend_healthy", "1 if the backend passes health checks."),
		available:      perBackend("switchyard_backend_available", "1 if the backend may receive traffic (healthy, breaker not open)."),
		prefillRate:    perBackend("switchyard_backend_prefill_tokens_per_second", "Learned prefill throughput estimate."),
		bytesPerToken:  global("switchyard_bytes_per_token", "Learned request bytes per prompt token."),
		indexEntries:   global("switchyard_prefix_index_entries", "Block and backend pairs held in the prefix index."),
		admitted:       global("switchyard_admission_in_flight", "Requests currently admitted."),
		queued:         global("switchyard_admission_queued", "Requests waiting for admission."),
	}
}

func (c *stateCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.inFlight, c.pendingPrefill, c.decoding, c.healthy, c.available, c.prefillRate,
		c.bytesPerToken, c.indexEntries, c.admitted, c.queued,
	} {
		ch <- d
	}
}

func (c *stateCollector) Collect(ch chan<- prometheus.Metric) {
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	flag := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	for _, b := range c.pool.Backends() {
		load := b.Load()
		gauge(c.inFlight, float64(load.InFlight), b.ID())
		gauge(c.pendingPrefill, float64(load.PendingPrefillTokens), b.ID())
		gauge(c.decoding, float64(load.Decoding), b.ID())
		gauge(c.healthy, flag(b.Healthy()), b.ID())
		gauge(c.available, flag(b.Available()), b.ID())
		gauge(c.prefillRate, c.estimator.PrefillRate(b.Index()), b.ID())
	}
	gauge(c.bytesPerToken, c.estimator.BytesPerToken())
	gauge(c.indexEntries, float64(c.index.Len()))
	st := c.admission.Stats()
	gauge(c.admitted, float64(st.InFlight))
	gauge(c.queued, float64(st.Queued))
}
