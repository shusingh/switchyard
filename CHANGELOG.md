# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/).

## [0.1.0] - unreleased

First release.

### Added

- OpenAI-compatible routing proxy for chat and text completions, streaming
  and non-streaming, with client cancellation propagated to the backend.
- Prefix tracking without a tokenizer: canonical request bytes hashed in
  chained blocks, and a per-backend index bounded by each replica's KV
  capacity, with LRU eviction, a TTL, and restart generations.
- Precise mode: keys identical to vLLM's `sha256_cbor` block hashes, kept in
  sync by the engines' KV cache events over ZeroMQ.
- Routing policies: `prefix_affinity` (default), `estimated_ttft`,
  `least_loaded`, `p2c`, `round_robin`, `random`.
- Admission control: per-tenant token buckets, weighted fair queuing, and
  early `429` with `Retry-After`.
- Resilience: active health checks, circuit breakers, and retries on another
  backend before the first byte, within a retry budget.
- Prometheus metrics, structured access logs without prompt content, and
  optional headers explaining each routing decision.
- `simengine`, a simulated vLLM-like engine with a KV cache, chunked prefill,
  continuous batching, fault injection, and an optional shared accelerator.
- `loadgen`, an open-loop load generator with agent-session, shared-prefix,
  and Mooncake trace workloads, and a report tool that groups trials.
- Scripts and a reference setup for benchmarking on four vLLM replicas.
