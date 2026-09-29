# GPU benchmark

Generated 2026-09-29T17:35Z by `scripts/bench-gpu.sh bench/results/gpu-agent "round_robin least_loaded prefix_affinity estimated_ttft estimated_ttft:precise" 3 -workload agent -duration 5m -rate 0.5`.
Four vLLM replicas of Qwen/Qwen2.5-1.5B-Instruct on one RTX 4090 (see deploy/vllm/README.md and ADR 0009).
Replicas share one GPU; results compare policies under identical conditions.

| Run | Trials | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| estimated_ttft-precise | 3 | 1657 (1657 to 1657) | 0 (0 to 0) | 55.0% (49.4% to 68.6%) | 1.32 s (720 ms to 2.13 s) | 6.94 s (4.62 s to 8.62 s) | 9.90 s (6.21 s to 11.64 s) | 74 ms (63 ms to 80 ms) | 183 (175 to 188) | 56.8% (52.5% to 60.9%) | 54.7% (51.0% to 59.2%) | 257 ms (190 ms to 316 ms) |
| estimated_ttft | 3 | 1657 (1657 to 1657) | 0 (0 to 0) | 64.7% (56.6% to 65.9%) | 688 ms (647 ms to 1.17 s) | 5.30 s (5.27 s to 6.04 s) | 7.64 s (7.03 s to 7.73 s) | 63 ms (60 ms to 73 ms) | 186 (184 to 188) | 62.7% (58.5% to 62.9%) | 63.5% (59.3% to 63.7%) | 179 ms (169 ms to 241 ms) |
| least_loaded | 3 | 1657 (1657 to 1657) | 0 (0 to 0) | 30.8% (30.3% to 30.9%) | 6.15 s (6.06 s to 6.44 s) | 14.30 s (13.42 s to 16.03 s) | 21.52 s (19.02 s to 21.60 s) | 84 ms (82 ms to 85 ms) | 164 (162 to 167) | 31.3% (31.0% to 31.8%) | 31.9% (31.7% to 32.3%) | 618 ms (501 ms to 620 ms) |
| prefix_affinity | 3 | 1657 (1657 to 1657) | 0 (0 to 0) | 92.6% (92.3% to 96.0%) | 241 ms (241 ms to 250 ms) | 1.44 s (1.15 s to 1.55 s) | 4.43 s (3.23 s to 4.87 s) | 41 ms (40 ms to 43 ms) | 197 (196 to 198) | 81.7% (81.6% to 82.9%) | 82.7% (82.5% to 83.7%) | 94 ms (93 ms to 102 ms) |
| round_robin | 3 | 1657 (1657 to 1657) | 0 (0 to 0) | 37.0% (36.4% to 38.1%) | 3.91 s (3.86 s to 4.81 s) | 17.36 s (14.91 s to 18.74 s) | 23.58 s (19.43 s to 28.18 s) | 81 ms (79 ms to 83 ms) | 168 (166 to 168) | 32.7% (32.5% to 33.1%) | 33.3% (33.1% to 33.6%) | 525 ms (480 ms to 545 ms) |
