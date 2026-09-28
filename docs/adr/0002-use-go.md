# ADR 0002: Use Go for the router, simulator, and load generator

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

The router sits on the request path of every inference call, so it needs low overhead, strong concurrency primitives, and a first-class HTTP stack. The simulator and load generator should share wire types and canonicalization code with it. The surrounding ecosystem (llm-d, Gateway API Inference Extension) is written in Go.

## Decision

Write all three binaries in Go, in one module, sharing `internal/` packages.

## Consequences

One toolchain, static binaries, and cheap goroutines for thousands of concurrent streams. Rust was rejected for slower iteration; Python for interpreter overhead and weaker concurrency on the hot path. Benchmark analysis and plotting may still use a small Python script, kept out of the Go module.
