# GPU benchmark

Generated 2026-09-29T23:34Z by `scripts/bench-gpu.sh bench/results/gpu-agent-rate-0.75 "round_robin prefix_affinity" 3 -workload agent -duration 5m -rate 0.75`.
Four vLLM replicas of Qwen/Qwen2.5-1.5B-Instruct on one RTX 4090 (see deploy/vllm/README.md and ADR 0009).
Replicas share one GPU; results compare policies under identical conditions.

| Run | Trials | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| prefix_affinity | 3 | 2402 (2402 to 2402) | 0 (0 to 0) | 28.6% (27.9% to 33.0%) | 8.13 s (5.73 s to 11.21 s) | 18.19 s (13.12 s to 21.92 s) | 22.36 s (16.72 s to 27.38 s) | 92 ms (83 ms to 99 ms) | 221 (206 to 234) | 59.1% (58.9% to 60.0%) | 59.9% (59.5% to 60.8%) | 614 ms (470 ms to 795 ms) |
| round_robin | 3 | 2402 (2363 to 2402) | 0 (0 to 11) | 15.1% (15.1% to 17.0%) | 20.62 s (18.82 s to 21.19 s) | 37.85 s (33.48 s to 54.35 s) | 42.80 s (42.39 s to 59.08 s) | 85 ms (84 ms to 101 ms) | 179 (159 to 186) | 31.3% (31.1% to 32.7%) | 31.9% (31.7% to 33.4%) | 956 ms (945 ms to 1.23 s) |
