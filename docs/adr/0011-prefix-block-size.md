# ADR 0011: Keep 128-byte prefix blocks in approximate mode

- **Status:** Accepted
- **Date:** 2026-09-29

## Context

Approximate mode hashes requests in fixed-size byte blocks (design.md section
6). Smaller blocks track the engines' 16-token blocks more finely but double
the index's size and the per-request hashing and matching work; larger blocks
are cheaper but lose the tail of every shared prefix.

The design set 128 bytes (about 23 tokens of the benchmark text) as the
default and called for a sweep of 64, 128, and 256 bytes.

## Decision

Keep 128 bytes. On the simulated agent workload (4 replicas, prefix_affinity,
1,657 requests each, `bench/results/sim-blocksize-*`):

| Block size | Cache hit rate | TTFT p50 | TTFT p99 |
|---|---:|---:|---:|
| 64 bytes | 87.7% | 52 ms | 558 ms |
| 128 bytes | 88.1% | 53 ms | 502 ms |
| 256 bytes | 87.4% | 53 ms | 594 ms |

The differences are within run-to-run variation. 128 bytes is at least as good
as the alternatives and keeps the index half the size of 64-byte blocks.

## Consequences

The default is unchanged and now justified by measurement. Prompts whose
prefixes diverge within a few tokens, where finer blocks would matter more,
were not tested; `routing.block_bytes` remains configurable.
