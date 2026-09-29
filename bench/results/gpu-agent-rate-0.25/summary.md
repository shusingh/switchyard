# GPU benchmark

Generated 2026-09-29T22:15Z by `scripts/bench-gpu.sh bench/results/gpu-agent-rate-0.25 "round_robin prefix_affinity" 3 -workload agent -duration 5m -rate 0.25`.
Four vLLM replicas of Qwen/Qwen2.5-1.5B-Instruct on one RTX 4090 (see deploy/vllm/README.md and ADR 0009).
Replicas share one GPU; results compare policies under identical conditions.

| Run | Trials | Requests | Failed | Goodput (TTFT <= 2s) | TTFT p50 | TTFT p90 | TTFT p99 | TPOT p50 | Output tok/s | Cache hit rate | Router-believed hit rate | TTFT prediction error p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| prefix_affinity | 3 | 761 (761 to 761) | 0 (0 to 0) | 100.0% (100.0% to 100.0%) | 144 ms (137 ms to 171 ms) | 244 ms (228 ms to 269 ms) | 615 ms (534 ms to 758 ms) | 23 ms (21 ms to 28 ms) | 126 (125 to 126) | 89.7% (88.9% to 90.0%) | 90.3% (89.4% to 90.3%) | 48 ms (46 ms to 59 ms) |
| round_robin | 3 | 761 (761 to 761) | 0 (0 to 0) | 99.3% (99.2% to 99.7%) | 509 ms (447 ms to 512 ms) | 1.25 s (1.21 s to 1.39 s) | 1.83 s (1.73 s to 1.89 s) | 44 ms (42 ms to 46 ms) | 121 (120 to 122) | 46.1% (45.1% to 48.0%) | 47.0% (46.0% to 48.6%) | 127 ms (127 ms to 132 ms) |
