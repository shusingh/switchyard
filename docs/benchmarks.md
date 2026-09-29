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

### GPU, agent workload

`scripts/bench-gpu.sh bench/results/gpu-agent "round_robin least_loaded prefix_affinity estimated_ttft estimated_ttft:precise" 3 -workload agent -duration 5m -rate 0.5`

Four vLLM replicas of Qwen2.5-1.5B-Instruct sharing one RTX 4090, 1,657
requests per trial, three trials per policy. Medians across trials, with the
range in parentheses. Full table: `bench/results/gpu-agent/summary.md`.

| Policy | Cache hit rate | TTFT p50 | TTFT p99 | Goodput (TTFT <= 2 s) | Output tok/s |
|---|---:|---:|---:|---:|---:|
| round_robin | 32.7% (32.5 to 33.1) | 3.91 s (3.86 to 4.81) | 23.58 s (19.43 to 28.18) | 37.0% | 168 |
| least_loaded | 31.3% (31.0 to 31.8) | 6.15 s (6.06 to 6.44) | 21.52 s (19.02 to 21.60) | 30.8% | 164 |
| prefix_affinity | 81.7% (81.6 to 82.9) | 241 ms (241 to 250) | 4.43 s (3.23 to 4.87) | 92.6% | 197 |
| estimated_ttft | 62.7% (58.5 to 62.9) | 688 ms (647 ms to 1.17 s) | 7.64 s (7.03 to 7.73) | 64.7% | 186 |
| estimated_ttft, precise | 56.8% (52.5 to 60.9) | 1.32 s (720 ms to 2.13 s) | 9.90 s (6.21 to 11.64) | 55.0% | 183 |

The cache hit rate is vLLM's own counter. No request failed.

**Findings.**

- prefix_affinity against the cache-blind baselines: 2.5 times the cache hit
  rate, TTFT p50 16 times lower than round_robin, and 2.5 times the goodput.
  The simulator predicted the same ordering.
- estimated_ttft, which matched prefix_affinity in simulation, falls between
  prefix_affinity and the baselines on the GPU. It routes 39% of prompt blocks
  away from the replica holding them (mean matched fraction 0.61 against
  prefix_affinity's 0.83) and its TTFT predictions are wider (p10 to p90
  signed error -0.7 s to +1.4 s against -0.5 s to +0.2 s).
- Cause: the four replicas share one GPU, and the estimator treats replicas
  as independent compute, so moving a request to a replica with a shorter
  queue looks faster when that replica actually competes for the same
  device. A simulator mode with shared compute (ADR 0012) reproduces the GPU
  ordering and hit rates; see the table below. Three device-aware changes to
  the estimator were tried and rejected (ADR 0012), so on shared
  accelerators prefix_affinity is the recommended policy.
- Precise mode did not help here, and its trials varied more. Its keys were
  verified identical to vLLM's, so the gap is in the routing decisions it
  feeds, not in index accuracy; it inherits the estimated_ttft behavior
  above.

### GPU, Mooncake tool-agent trace

`scripts/bench-gpu.sh bench/results/gpu-mooncake "round_robin least_loaded prefix_affinity estimated_ttft" 3 -workload mooncake -time-scale 4 -trace-duration 2m -max-prompt-tokens 15000 -max-output-tokens 64`

The first two minutes of the trace, stretched four times (548 requests over
8 minutes), outputs capped at 64 tokens. 95 prompts over 15,000 tokens were
dropped. Three trials, medians.

| Policy | Cache hit rate | TTFT p50 | TTFT p99 | Goodput (TTFT <= 2 s) |
|---|---:|---:|---:|---:|
| round_robin | 52.8% | 917 ms | 4.31 s | 80.3% |
| least_loaded | 52.5% | 979 ms | 4.08 s | 79.4% |
| prefix_affinity | 56.2% | 756 ms | 3.55 s | 84.9% |
| estimated_ttft | 55.3% | 873 ms | 3.80 s | 83.9% |

The gaps are small because most of this trace's reuse comes from prefixes
shared by nearly every request, which any policy's replicas soon cache;
per-session reuse, where routing matters, is a smaller share than in the
agent workload. prefix_affinity is still best on every column, with TTFT
p50 18% lower than round_robin's.

### Router accuracy

Across every approximate-mode GPU run above, the router's believed cache hit
rate (from its prefix index) is 0 to 1.0 point above vLLM's measured rate,
so index drift is not a factor in these results. Precise mode, fed by vLLM's
own KV events, came out 2.1 points below the measured rate, erring on the
side of predicting misses.

### Hot prefix

Two system prompts with Zipf skew 4, so one prompt dominates, at 1.0
sessions per second.

Simulated, independent replicas
(`scripts/bench-sim.sh bench/results/sim-hot-prefix-4 4 "least_loaded prefix_affinity estimated_ttft" -workload agent -duration 5m -rate 1.0 -apps 2 -app-skew 4`):

| Policy | Cache hit rate | TTFT p50 | TTFT p99 | Goodput (TTFT <= 2 s) |
|---|---:|---:|---:|---:|
| least_loaded | 65.2% | 210 ms | 981 ms | 100.0% |
| prefix_affinity | 62.2% | 20.42 s | 35.88 s | 18.7% |
| estimated_ttft | 85.1% | 59 ms | 501 ms | 100.0% |

prefix_affinity piles the dominant prefix onto one replica; estimated_ttft
replicates it as far as load requires and keeps the highest hit rate.

GPU, same workload, three trials, medians
(`scripts/bench-gpu.sh bench/results/gpu-hot-prefix "least_loaded prefix_affinity estimated_ttft" 3 -workload agent -apps 2 -app-skew 4 -rate 1.0 -duration 5m`):

| Policy | Cache hit rate | TTFT p50 | TTFT p99 | Goodput (TTFT <= 2 s) |
|---|---:|---:|---:|---:|
| least_loaded | 58.4% | 10.24 s | 22.88 s | 23.0% |
| prefix_affinity | 62.5% | 10.65 s | 15.98 s | 24.0% |
| estimated_ttft | 62.3% | 7.32 s | 19.61 s | 29.5% |

At this rate the shared GPU is saturated under every policy, and the
simulated advantage does not carry over: there is no idle compute to spread
the hot prefix onto (ADR 0012).

### Simulated, agent workload, four replicas on one shared device

`SHARED_DEVICE=1 scripts/bench-sim.sh bench/results/sim-agent-4-shared 4 "round_robin least_loaded prefix_affinity estimated_ttft" -workload agent -duration 5m -rate 0.5`

Simulated engines that execute one step at a time on a shared simulated
accelerator (ADR 0012). One run per policy; GPU hit rates in parentheses.

| Policy | Cache hit rate | TTFT p50 | TTFT p99 | Goodput (TTFT <= 2 s) |
|---|---:|---:|---:|---:|
| round_robin | 31.3% (32.7%) | 18.37 s | 47.53 s | 17.9% |
| least_loaded | 30.4% (31.3%) | 19.30 s | 42.17 s | 17.6% |
| prefix_affinity | 77.8% (81.7%) | 264 ms | 6.45 s | 87.5% |
| estimated_ttft | 54.1% (62.7%) | 6.39 s | 23.61 s | 42.7% |

The ordering and hit rates match the GPU. Latencies are higher because the
model fully serializes the replicas' steps, while the GPU overlaps some of
their work.
