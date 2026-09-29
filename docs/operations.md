# Operating Switchyard

Notes for running the router in front of real model servers. Every
configuration option is documented in
[configs/switchyard.example.yaml](../configs/switchyard.example.yaml).

## Endpoints

| Path | Purpose |
|---|---|
| `POST /v1/chat/completions`, `POST /v1/completions` | Routed inference, streaming or not |
| `GET /v1/models` | Models served by the pool |
| `GET /healthz` | Liveness: the process is serving HTTP |
| `GET /readyz` | Readiness: at least one backend is healthy; `503` otherwise |
| `GET /metrics` | Prometheus metrics |

Point load balancer health checks at `/readyz` and process supervisors at
`/healthz`.

## Choosing a policy

- **Replicas sharing an accelerator** (several engines on one GPU): use
  `prefix_affinity`, the default. It measured best on the reference setup
  ([benchmarks](benchmarks.md)).
- **Replicas with their own accelerators and skewed traffic** (one system
  prompt dominating): use `estimated_ttft`, which replicates a hot prefix
  onto more replicas as load requires. Set `routing.kv_capacity_tokens` and
  `routing.prefill_tokens_per_second` from the engines' startup logs and a
  measured TTFT; throughput is refined from observations after that.
- Set `routing.bytes_per_token` from the deployment's own traffic: the ratio
  of request bytes to `usage.prompt_tokens`. It is refined from responses,
  but a close starting value shortens warm-up.

## Startup and shutdown

- Backends start unhealthy. The first successful health check marks a backend
  healthy at once; after that, `unhealthy_threshold` consecutive failures
  remove it and `healthy_threshold` successes restore it.
- On `SIGTERM` or interrupt, the router stops accepting connections and lets
  in-flight requests finish for up to `server.shutdown_timeout`, then closes
  what remains. Health checks keep running during the drain. Remove the
  instance from its load balancer before sending the signal.

## Failure handling

- **Retries.** A request that fails before any byte reached the client
  (connection failure, `502`, `503`, `504`) is retried once on a different
  backend (`proxy.max_retries`). Retries are capped at `proxy.retry_budget_ratio`
  of recent requests so a failing pool is not hit with a retry storm.
  Streams that have started are never retried.
- **Circuit breakers.** `proxy.breaker_failures` consecutive failures open a
  backend's breaker for `proxy.breaker_cooldown`; one trial request then
  decides whether it closes. Client disconnects do not count as failures.
- **Backend restarts.** A backend that goes unhealthy and comes back gets a
  new generation, and everything the prefix index believed about its cache is
  discarded. A restart quick enough to pass every health check goes unnoticed
  in approximate mode until the stale entries expire (`routing.index_ttl`);
  precise mode notices the restarted event stream and clears its entries.
- **Overload.** With `admission` configured, requests beyond
  `admission.max_in_flight` wait in per-tenant queues for up to
  `admission.queue_timeout`, then receive `429` with `Retry-After`.

## What to alert on

| Signal | Metric | Meaning |
|---|---|---|
| No healthy backends | `switchyard_backend_healthy` | Every backend failing health checks; `/readyz` returns `503` |
| Error rate | `switchyard_requests_total` by `code` | Rising `5xx` from backends or retries exhausted |
| Retry budget use | `switchyard_retries_total` | Sustained retries mean a backend is flapping |
| Shedding | `switchyard_admission_rejections_total` | Capacity is short of demand |
| Latency | `switchyard_ttft_seconds`, `switchyard_tpot_seconds` | Per backend, as clients see it |
| Estimator drift | `switchyard_ttft_prediction_error_seconds` | Large errors mean the cost model's inputs are off |
| Router cost | `switchyard_route_decision_seconds` | Time spent choosing a backend |

`switchyard_prefix_match_ratio` shows how much of each prompt the router
believed cached. Compare it with the engines' own
`vllm:prefix_cache_hits_total / vllm:prefix_cache_queries_total`; a gap of
more than a few points means the index and the caches disagree, usually
because `kv_capacity_tokens` is set too high.

## Precise mode

`routing.prefix_mode: precise` keys the index on the engines' own block
hashes, driven by their KV cache events. It requires, per backend,
`kv_events_endpoint`, and on every vLLM replica
`--enable-prefix-caching --prefix-caching-hash-algo sha256_cbor` with KV
events enabled; see [deploy/vllm/README.md](../deploy/vllm/README.md). vLLM
publishes events when its engine loop steps, so an idle engine delays them.
On the reference setup precise mode did not improve routing over the
default approximate mode, which needs no engine configuration.

## Privacy

Prompt and completion content is never logged. Access logs record sizes,
timings, the chosen backend, and the routing prediction.
