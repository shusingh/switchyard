# ADR 0009: Benchmark on four small replicas with fixed 1 GiB KV caches

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

GPU benchmarks run on one RTX 4090 (24 GB, of which about 3 GB is used by the
Windows desktop). Routing policies can only be compared across several
replicas, so the replicas must share the GPU. Prefix-aware routing matters most
when the set of hot prefixes exceeds one replica's KV cache but fits in the
pool's combined cache, so cache capacity must be small, exact, and identical
across replicas and runs.

## Decision

Run four replicas of `Qwen/Qwen2.5-1.5B-Instruct` (bf16) under vLLM 0.30.0,
each with `--kv-cache-memory-bytes 1G`, which gives exactly 2,340 blocks of 16
tokens (37,440 tokens) per replica and 149,760 tokens across the pool.
Benchmark workloads are sized against these numbers: the hot working set is
chosen to be several times one replica's capacity and below the pool's.

## Consequences

- Capacity is deterministic, so the simulator can be configured with the same
  block counts and its results compared directly with the GPU runs.
- A 1.5B model prefills quickly, so absolute latencies are small. The
  comparison between policies under identical conditions is what matters;
  results must not be presented as production latencies.
- Replicas share compute. A busy replica slows the others in a way separate
  GPUs would not. This is stated with every result.
- GPU memory headroom is small (23.9 of 24.5 GB in use). Larger models or more
  replicas need a different setup, recorded in a new ADR.
