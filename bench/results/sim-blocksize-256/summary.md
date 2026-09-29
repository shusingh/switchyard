# Simulated benchmark: 4 replicas

Generated 2026-09-29T15:34Z by `scripts/bench-sim.sh bench/results/sim-blocksize-256 4 "prefix_affinity" -workload agent -duration 5m -rate 0.5`.
Simulated engines (internal/sim, default cost model); not GPU measurements.

| Run | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| prefix_affinity (4 replicas, simulated) | 1657 | 0 | 100.0% | 53 ms | 176 ms | 594 ms | 8 ms | 204 | 87.4% | 87.9% | 5 ms |
