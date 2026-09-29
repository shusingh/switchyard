# vLLM replicas

Scripts for running the model servers Switchyard routes to during development
and GPU benchmarks. On Windows they run inside WSL2; on Linux, natively.

```bash
make vllm-install   # one time: creates ~/venvs/vllm and installs vLLM
make vllm-up        # start the replicas one at a time, wait until healthy
make vllm-status    # health of each replica
make vllm-down      # stop them
```

Logs are in `~/.switchyard/vllm/<port>.log` inside the Linux environment
(`bash deploy/vllm/replicas.sh logs 50` prints the tail of each).

## Reference setup

Recorded on 2026-09-28. Benchmark results must state this configuration.

| Item | Value |
|---|---|
| GPU | NVIDIA GeForce RTX 4090, 24 GB, driver 610.74 (Windows host) |
| Environment | WSL 2.7.14, Ubuntu 24.04.5, kernel 6.18 |
| vLLM | 0.30.0 |
| PyTorch | 2.13.0+cu132 (CUDA 13.2) |
| Model | `Qwen/Qwen2.5-1.5B-Instruct`, bf16 weights (2.98 GiB per replica) |
| Replicas | 4, on ports 8001 to 8004, sharing the one GPU |
| KV cache | 1 GiB per replica (`--kv-cache-memory-bytes 1G`) |
| KV capacity | 2,340 blocks of 16 tokens = **37,440 tokens per replica**, 149,760 in the pool |
| Max context | 16,384 tokens (`--max-model-len`) |
| Prefix caching | Enabled; block size 16; hash algorithm `sha256_cbor` (reproducible outside Python; the default `sha256` hashes a pickle) |
| KV events | Published over ZeroMQ, topic `kv-events`, replica N on port 5601+N |
| GPU memory in use | 23.9 of 24.5 GB with all four replicas up (about 3.1 GB is the Windows desktop) |

The exact command line per replica:

```bash
VLLM_USE_FLASHINFER_SAMPLER=0 VLLM_SERVER_DEV_MODE=1 \
vllm serve Qwen/Qwen2.5-1.5B-Instruct \
  --host 0.0.0.0 --port 8001 \
  --kv-cache-memory-bytes 1G \
  --max-model-len 16384 \
  --enable-prefix-caching   --prefix-caching-hash-algo sha256_cbor   --kv-events-config '{"enable_kv_cache_events": true, "publisher": "zmq", "endpoint": "tcp://*:5601", "topic": "kv-events"}'
```

## Verified behavior

| Check | Result |
|---|---|
| `/health` from Windows via `localhost:800N` | 200 on all four replicas |
| Streaming chat completions with `stream_options.include_usage` | Works; the first chunk carries the role and an **empty** content delta, so time to first token must be measured at the first non-empty delta |
| `POST /tokenize` with chat messages | Works; applies the chat template (Qwen injects a default system prompt when none is given) |
| `/metrics` | Exposes `vllm:num_requests_running`, `vllm:num_requests_waiting`, `vllm:kv_cache_usage_perc`, `vllm:prefix_cache_queries_total`, `vllm:prefix_cache_hits_total`, and `vllm:cache_config_info` (includes `num_gpu_blocks`) |
| Prefix cache reuse | A repeated 7,105-token prompt hit 7,088 tokens (the final partial block is not cached); latency fell from 568 ms to 303 ms for a one-token completion |
| `POST /reset_prefix_cache` | 200 with `VLLM_SERVER_DEV_MODE=1`; 404 without it |

## Why the setup looks like this

- **Fixed KV cache size.** `--kv-cache-memory-bytes` makes capacity exact and
  identical across replicas and runs. Sizing by `--gpu-memory-utilization`
  leaves the cache with whatever memory profiling finds left over, which varies.
  It also deliberately keeps each cache small: prefix-aware routing matters
  when the hot working set exceeds one replica's cache (see
  [ADR 0009](../../docs/adr/0009-benchmark-replica-configuration.md)).
- **Sequential startup.** vLLM measures free GPU memory at startup; concurrent
  startups each see memory the others are about to claim.
- **`VLLM_USE_FLASHINFER_SAMPLER=0`.** FlashInfer's sampler is compiled on first
  use and needs the full CUDA toolkit (`nvcc`). The precompiled PyTorch sampler
  avoids that, and sampling cost is negligible next to prefill for this study.
- **`VLLM_SERVER_DEV_MODE=1`.** Exposes `/reset_prefix_cache` so each benchmark
  trial starts from an empty cache. Development mode also exposes other
  administrative endpoints, so replicas must never be reachable beyond this
  machine.

## Troubleshooting

| Symptom in the log | Cause and fix |
|---|---|
| `Failed to find C compiler` | `torch.compile` needs gcc: install `build-essential` and `python3-dev` in the Linux environment |
| `Could not find nvcc` | FlashInfer JIT compilation; keep `VLLM_USE_FLASHINFER_SAMPLER=0` or install the CUDA toolkit |
| `larger than the available KV cache memory` | The KV cache cannot hold one max-length request; raise `KV_CACHE_BYTES` or lower `MAX_MODEL_LEN` |
| CUDA out of memory at startup | Another process holds GPU memory; stop stray servers (`make vllm-down`) and check `nvidia-smi` |
