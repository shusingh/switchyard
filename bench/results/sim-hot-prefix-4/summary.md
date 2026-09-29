# Simulated benchmark: 4 replicas

Generated 2026-09-29T15:32Z by `scripts/bench-sim.sh bench/results/sim-hot-prefix-4 4 "least_loaded prefix_affinity estimated_ttft" -workload agent -duration 5m -rate 1.0 -apps 2 -app-skew 4`.
Simulated engines (internal/sim, default cost model); not GPU measurements.

| Run | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| estimated_ttft (4 replicas, simulated) | 3030 | 0 | 100.0% | 59 ms | 236 ms | 501 ms | 9 ms | 428 | 85.1% | 85.7% | 6 ms |
| least_loaded (4 replicas, simulated) | 3030 | 0 | 100.0% | 210 ms | 558 ms | 981 ms | 11 ms | 425 | 65.2% | 65.7% | 34 ms |
| prefix_affinity (4 replicas, simulated) | 3030 | 0 | 18.7% | 20.42 s | 34.23 s | 35.88 s | 37 ms | 236 | 62.2% | 62.7% | 193 ms |
