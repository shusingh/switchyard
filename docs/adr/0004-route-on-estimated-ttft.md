# ADR 0004: Route on estimated time to first token

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

Existing routers combine cache affinity and load as unitless weighted scores or threshold rules. Weights must be tuned per workload and cannot be validated against reality. Pure affinity creates hot spots when one prefix is popular.

## Decision

The `estimated_ttft` policy computes, per backend, the predicted time to first token in seconds: queued prefill tokens plus this request's uncached tokens, divided by a calibrated prefill rate, plus a decode-interference term. It picks the minimum, with an imbalance guard and randomized tie-breaking (design.md section 9).

## Consequences

Every routing decision yields a prediction that can be compared with the measured TTFT, so the model is testable and its error is observable as a metric. Popular prefixes replicate across backends exactly as far as load requires, without special cases. Calibration adds a startup probe and an EWMA per backend.
