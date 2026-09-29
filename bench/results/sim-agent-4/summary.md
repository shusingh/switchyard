# Simulated benchmark: 4 replicas

Generated 2026-09-29T15:02Z by `scripts/bench-sim.sh bench/results/sim-agent-4 4 "random round_robin least_loaded p2c prefix_affinity estimated_ttft" -workload agent -duration 5m -rate 0.5`.
Simulated engines (internal/sim, default cost model); not GPU measurements.

| Run | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| estimated_ttft (4 replicas, simulated) | 1657 | 0 | 100.0% | 58 ms | 156 ms | 529 ms | 9 ms | 204 | 87.2% | 87.6% | 6 ms |
| least_loaded (4 replicas, simulated) | 1657 | 0 | 100.0% | 318 ms | 695 ms | 1.12 s | 8 ms | 202 | 44.8% | 45.2% | 38 ms |
| p2c (4 replicas, simulated) | 1657 | 0 | 99.8% | 362 ms | 772 ms | 1.35 s | 11 ms | 200 | 43.3% | 43.7% | 57 ms |
| prefix_affinity (4 replicas, simulated) | 1657 | 0 | 100.0% | 59 ms | 168 ms | 515 ms | 10 ms | 203 | 87.8% | 88.3% | 7 ms |
| random (4 replicas, simulated) | 1657 | 0 | 98.8% | 377 ms | 979 ms | 2.12 s | 9 ms | 201 | 43.0% | 43.5% | 66 ms |
| round_robin (4 replicas, simulated) | 1657 | 0 | 100.0% | 280 ms | 657 ms | 939 ms | 8 ms | 203 | 45.9% | 46.3% | 45 ms |
