# Architecture decision records

Each file records one decision in the Context, Decision, Consequences format.
Accepted ADRs are not edited; a new ADR supersedes an old one. See
[ADR 0001](0001-record-architecture-decisions.md).

| ADR | Title | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-use-go.md) | Use Go for the router, simulator, and load generator | Accepted |
| [0003](0003-approximate-byte-block-prefix-hashing.md) | Key prefixes by hashing canonical bytes, not tokens | Accepted |
| [0004](0004-route-on-estimated-ttft.md) | Route on estimated time to first token | Accepted |
| [0005](0005-build-a-simulated-engine.md) | Build a simulated engine alongside real benchmarks | Accepted |
| [0006](0006-open-loop-load-generation.md) | Generate load open-loop | Accepted |
| [0007](0007-standard-library-first.md) | Prefer the standard library; justify every dependency | Accepted |
| [0008](0008-go-1-27-toolchain.md) | Target the Go 1.27 toolchain | Accepted |
| [0009](0009-benchmark-replica-configuration.md) | Benchmark on four small replicas with fixed 1 GiB KV caches | Accepted |
| [0010](0010-precise-mode-dependencies.md) | ZeroMQ and msgpack libraries for precise mode | Accepted |
