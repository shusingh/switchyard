package sim

import (
	"fmt"
	"net/http"
	"strings"
)

// handleMetrics serves the subset of vLLM's Prometheus metrics that
// Switchyard and the benchmark tooling read, with vLLM's names and labels, so
// the same scraping code works against both.
func (e *Engine) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	s := e.Stats()
	labels := fmt.Sprintf(`engine="0",model_name=%q`, e.opts.Model)
	usage := float64(e.sched.pinnedBlocks.Load()) / float64(e.opts.Cost.CapacityBlocks)

	var b strings.Builder
	metric := func(kind, name string, value float64) {
		fmt.Fprintf(&b, "# TYPE %s %s\n%s{%s} %g\n", name, kind, name, labels, value)
	}
	metric("gauge", "vllm:num_requests_running", float64(s.Running))
	metric("gauge", "vllm:num_requests_waiting", float64(s.Waiting))
	metric("gauge", "vllm:kv_cache_usage_perc", usage)
	metric("counter", "vllm:prefix_cache_queries_total", float64(s.PromptTokens))
	metric("counter", "vllm:prefix_cache_hits_total", float64(s.CachedPromptTokens))
	fmt.Fprintf(&b, "# TYPE vllm:cache_config_info gauge\nvllm:cache_config_info{%s,block_size=\"%d\",num_gpu_blocks=\"%d\"} 1\n",
		labels, e.opts.Cost.BlockTokens, e.opts.Cost.CapacityBlocks)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(b.String()))
}
