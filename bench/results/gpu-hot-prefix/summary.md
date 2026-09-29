# GPU benchmark

Generated 2026-09-29T19:14Z by `scripts/bench-gpu.sh bench/results/gpu-hot-prefix "least_loaded prefix_affinity estimated_ttft" 3 -workload agent -apps 2 -app-skew 4 -rate 1.0 -duration 5m`.
Four vLLM replicas of Qwen/Qwen2.5-1.5B-Instruct on one RTX 4090 (see deploy/vllm/README.md and ADR 0009).
Replicas share one GPU; results compare policies under identical conditions.

| Run | Trials | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| estimated_ttft | 3 | 3030 (3030 to 3030) | 0 (0 to 0) | 29.5% (28.2% to 30.2%) | 7.32 s (7.31 s to 7.53 s) | 16.67 s (16.47 s to 16.86 s) | 19.61 s (19.24 s to 19.61 s) | 100 ms (99 ms to 100 ms) | 291 (290 to 291) | 62.3% (62.1% to 62.8%) | 62.9% (62.8% to 63.5%) | 433 ms (380 ms to 450 ms) |
| least_loaded | 3 | 3030 (3030 to 3030) | 0 (0 to 0) | 23.0% (21.7% to 23.5%) | 10.24 s (9.97 s to 10.30 s) | 18.58 s (18.36 s to 19.14 s) | 22.88 s (22.49 s to 22.98 s) | 100 ms (99 ms to 100 ms) | 282 (278 to 283) | 58.4% (58.3% to 58.6%) | 59.0% (58.9% to 59.2%) | 560 ms (516 ms to 565 ms) |
| prefix_affinity | 3 | 3030 (3030 to 3030) | 0 (0 to 0) | 24.0% (22.8% to 24.3%) | 10.65 s (10.32 s to 13.04 s) | 13.64 s (13.48 s to 17.29 s) | 15.98 s (15.92 s to 19.09 s) | 21 ms (20 ms to 23 ms) | 334 (312 to 336) | 62.5% (62.3% to 62.5%) | 63.1% (62.9% to 63.1%) | 560 ms (426 ms to 646 ms) |
