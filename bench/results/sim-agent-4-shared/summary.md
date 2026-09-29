# Simulated benchmark: 4 replicas

Generated 2026-09-29T18:28Z by `scripts/bench-sim.sh bench/results/sim-agent-4-shared 4 "round_robin least_loaded prefix_affinity estimated_ttft" -workload agent -duration 5m -rate 0.5`.
Simulated engines (internal/sim, default cost model); not GPU measurements.

| Run | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| estimated_ttft (4 replicas, simulated) | 1657 | 0 | 42.7% | 6.39 s | 18.47 s | 23.61 s | 125 ms | 148 | 54.1% | 54.8% | 706 ms |
| least_loaded (4 replicas, simulated) | 1657 | 0 | 17.6% | 19.30 s | 34.16 s | 42.17 s | 143 ms | 124 | 30.4% | 31.0% | 1.62 s |
| prefix_affinity (4 replicas, simulated) | 1657 | 0 | 87.5% | 264 ms | 2.45 s | 6.45 s | 64 ms | 192 | 77.8% | 78.9% | 87 ms |
| round_robin (4 replicas, simulated) | 1657 | 0 | 17.9% | 18.37 s | 35.12 s | 47.53 s | 140 ms | 124 | 31.3% | 31.8% | 1.52 s |
