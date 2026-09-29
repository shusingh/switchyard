# GPU benchmark

Generated 2026-09-29T21:35Z by `scripts/bench-gpu.sh bench/results/gpu-mooncake "round_robin least_loaded prefix_affinity estimated_ttft" 3 -workload mooncake -time-scale 4 -trace-duration 2m -max-prompt-tokens 15000 -max-output-tokens 64`.
Four vLLM replicas of Qwen/Qwen2.5-1.5B-Instruct on one RTX 4090 (see deploy/vllm/README.md and ADR 0009).
Replicas share one GPU; results compare policies under identical conditions.

| Run | Trials | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| estimated_ttft | 3 | 548 (548 to 548) | 0 (0 to 0) | 83.9% (80.7% to 86.5%) | 873 ms (853 ms to 893 ms) | 2.45 s (2.31 s to 2.71 s) | 3.80 s (3.56 s to 4.12 s) | 42 ms (41 ms to 44 ms) | 39 (39 to 39) | 55.3% (55.2% to 55.5%) | 55.6% (55.4% to 55.8%) | 391 ms (389 ms to 419 ms) |
| least_loaded | 3 | 548 (548 to 548) | 0 (0 to 0) | 79.4% (78.8% to 81.8%) | 979 ms (924 ms to 993 ms) | 2.67 s (2.47 s to 2.71 s) | 4.08 s (4.05 s to 4.29 s) | 43 ms (42 ms to 43 ms) | 39 (39 to 39) | 52.5% (52.0% to 53.0%) | 53.0% (52.8% to 53.3%) | 419 ms (411 ms to 436 ms) |
| prefix_affinity | 3 | 548 (548 to 548) | 0 (0 to 0) | 84.9% (84.1% to 88.5%) | 756 ms (749 ms to 871 ms) | 2.25 s (2.03 s to 2.36 s) | 3.55 s (3.40 s to 3.83 s) | 32 ms (27 ms to 41 ms) | 39 (39 to 39) | 56.2% (56.0% to 56.3%) | 56.2% (55.8% to 56.3%) | 362 ms (334 ms to 373 ms) |
| round_robin | 3 | 548 (548 to 548) | 0 (0 to 0) | 80.3% (78.5% to 81.0%) | 917 ms (902 ms to 949 ms) | 2.61 s (2.52 s to 2.62 s) | 4.31 s (3.94 s to 4.39 s) | 44 ms (43 ms to 45 ms) | 39 (39 to 39) | 52.8% (52.4% to 52.8%) | 53.1% (53.0% to 53.5%) | 417 ms (395 ms to 448 ms) |
