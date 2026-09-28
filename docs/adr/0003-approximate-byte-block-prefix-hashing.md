# ADR 0003: Key prefixes by hashing canonical bytes, not tokens

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

vLLM caches KV blocks keyed by token IDs after chat templating. Reproducing that in the router needs the exact tokenizer and chat template per model, which adds heavy, model-specific dependencies and latency. The router only needs a stable key with the prefix property: requests that share a leading prefix share leading keys.

## Decision

In v1, build a canonical byte stream from the request (design.md section 6), split it into fixed-size byte blocks, and chain-hash the blocks with `hash/maphash`. Track what was routed where in an approximate index. A precise mode driven by vLLM KV events is a stretch goal, gated on a spike proving hash compatibility.

## Consequences

No tokenizer dependency and engine-agnostic behavior. The index can drift from true cache state when an engine evicts under memory pressure; drift is measured in the GPU benchmarks and bounded by TTLs, capacity budgets, and backend generations.
