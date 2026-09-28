# ADR 0006: Generate load open-loop

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

Closed-loop load generators send the next request only after the previous one completes. When the system slows, they slow too, hiding queueing delay (coordinated omission) and flattering every policy under test.

## Decision

`loadgen` schedules arrivals independently of completions, from a Poisson process or from trace timestamps, and records scheduled, sent, first-token, and completion times for every request.

## Consequences

Latency percentiles include queueing delay, as a real user would experience it. Overload is visible as growing latency and errors rather than silently reduced throughput.
