# ADR 0005: Build a simulated engine alongside real benchmarks

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

GPU benchmarks are slow, need specific hardware, and cannot run in CI. Routing policies still need fast, deterministic testing, fault injection, and experiments with more replicas than one GPU can host.

## Decision

Build `simengine`, an OpenAI-compatible server with a block-level prefix cache and a latency model, exposing vLLM-shaped metrics. Use it for integration tests, fault injection, and policy experiments. Validate its trends against real vLLM in Phase 7.

## Consequences

CI can exercise the full request path without a GPU. The simulator is extra code to maintain, and its results are always labeled as simulated and never presented as hardware measurements.
