# Benchmarks

This document describes how Switchyard is measured, then reports the results.
The method was written before the results, and every number below can be
reproduced with the commands given.

## What is being compared

Six routing policies behind the same router build, on the same workload:

| Policy | Uses cache state | Uses load |
|---|:-:|:-:|
| `random` | | |
| `round_robin` | | |
| `least_loaded` | | yes |
| `p2c` | | yes |
| `prefix_affinity` | yes | ties only |
| `estimated_ttft` | yes | yes |

The router tracks prefixes under every policy, so per-request overhead is the
same for all of them. The results compare routing policies under identical
conditions. They are not claims about production deployments.

## Setups

### GPU (headline results)

| Item | Value |
|---|---|
| GPU | One NVIDIA RTX 4090, 24 GB (Windows 11 host, WSL2) |
| Engine | vLLM 0.30.0, PyTorch 2.13 (CUDA 13.2) |
| Model | `Qwen/Qwen2.5-1.5B-Instruct`, bf16 |
| Replicas | 4, sharing the GPU |
| KV cache | 1 GiB per replica: 2,340 blocks of 16 tokens, 37,440 tokens; 149,760 across the pool |
| Context | 16,384 tokens |

Exact commands and flags: [deploy/vllm/README.md](../deploy/vllm/README.md);
rationale: [ADR 0009](adr/0009-benchmark-replica-configuration.md).

**Caveat:** the four replicas share one GPU, so a busy replica slows the
others in a way separate GPUs would not. This affects every policy equally,
but absolute latencies are not those of a multi-GPU deployment, and a 1.5B
model prefills far faster than the large models production routers serve.

### Simulated

`cmd/simengine` replicas with the default cost model, which matches the GPU
setup's cache capacity and the synthetic text's token ratio (design.md
section 14.1). The simulator models caching, chunked prefill, and batching;
it is used to explore larger pools and to cross-check the GPU trends.
Simulated results are always labeled as simulated.

## Workloads

- **`agent`** (default): synthetic tool-using agent sessions. Sessions arrive
  as a Poisson process (0.5 per second for 5 minutes). Each belongs to one of
  16 apps (Zipf popularity) whose 4,000-token system prompt it shares with
  other sessions of that app, then grows by a tool call and a tool result per
  turn (4 to 16 turns, ending before 12,000 words of context). Think time
  between turns is log-normal around 2 seconds, with 10% long pauses of 20 to
  60 seconds. The shared working set (16 apps x 4,000 tokens) exceeds one
  replica's cache and fits in the pool's.
- **`mooncake`**: the public tool-agent trace from Mooncake (FAST '25),
  replayed with its real prefix-sharing structure; each trace block becomes a
  deterministic block of text, so requests sharing trace blocks share text.
  Time is stretched to fit one GPU, and prompts over the context window are
  dropped and counted.
- **`prefix`**: requests sharing one of a few long prefixes, as in vLLM's
  `prefix_repetition` benchmark.

Synthetic text is built from words that Qwen2.5's tokenizer encodes as
exactly one token each (verified word by word), so prompt sizes in tokens are
known exactly.

## Procedure

- **Open loop.** Sessions start on schedule regardless of how earlier
  requests fared, so latency includes queueing delay. Within a session, each
  turn waits for the previous response plus its think time.
- **Identical inputs.** Every policy replays the same seeded workload.
- **Cold start per trial.** Before each GPU trial the router is restarted and
  every replica's prefix cache is reset (`POST /reset_prefix_cache`).
  Simulated trials start fresh engines.
- **Repeated trials.** GPU results report the median of three trials, with
  the range.
- **Fixed output length.** Requests set `ignore_eos`, so every request
  generates exactly its configured number of tokens under every policy.

## Metrics

- **TTFT** (time to first token) p50, p90, p99, measured by the client at the
  first chunk carrying generated text.
- **TPOT** (time per output token after the first), p50.
- **Goodput:** the fraction of requests that succeed with TTFT within 2 s.
- **Cache hit rate:** the fraction of prompt tokens served from cache,
  from the engines' own `prefix_cache_hits` and `prefix_cache_queries`
  counters, not from the router.
- **Router-believed hit rate:** the fraction of prompt blocks the router
  believed were cached where it sent them; the gap to the measured rate is
  the index's drift.
- **TTFT prediction error:** the median absolute difference between the
  router's prediction and the measured TTFT.

## Router overhead

Measured on Linux (Go's clock on Windows cannot resolve these durations) with
a 4 KB request against instant backends, open loop at 1,000 requests per
second:

| Path | p50 | p99 |
|---|---:|---:|
| Direct to backend | 101 µs | 213 µs |
| Through Switchyard | 205 µs | 685 µs |
| Added by the router | 104 µs | 472 µs |

Reproduce: `go test -run='^$' -bench=LatencyAt1000RPS -benchtime=1x ./internal/server/`
on Linux.

## Results

Results are added here as they are measured, with the command that produced
each table.
