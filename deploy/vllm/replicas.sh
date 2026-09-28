#!/usr/bin/env bash
# Starts, stops, or reports on N vLLM replicas sharing one GPU.
#
# Usage: replicas.sh start|stop|status|logs [lines]
#
# Replicas start one at a time: vLLM profiles free GPU memory at startup, so
# concurrent startups can each see memory the others are about to claim.
#
# Each replica gets a fixed KV cache size (--kv-cache-memory-bytes) rather than
# a share of GPU memory, so cache capacity is exact and identical across
# replicas and runs. Keeping it small is deliberate: prefix-aware routing only
# matters when the hot working set exceeds one replica's cache
# (docs/engineering/design.md section 14.3).
set -euo pipefail

VENV_DIR="${VENV_DIR:-$HOME/venvs/vllm}"
MODEL="${MODEL:-Qwen/Qwen2.5-1.5B-Instruct}"
REPLICAS="${REPLICAS:-4}"
BASE_PORT="${BASE_PORT:-8001}"
KV_CACHE_BYTES="${KV_CACHE_BYTES:-1G}"
MAX_MODEL_LEN="${MAX_MODEL_LEN:-16384}"
STARTUP_TIMEOUT_S="${STARTUP_TIMEOUT_S:-600}"
RUN_DIR="${RUN_DIR:-$HOME/.switchyard/vllm}"

# FlashInfer's sampler is compiled on first use and needs the full CUDA
# toolkit (nvcc), which a plain WSL setup does not have. The PyTorch sampler is
# precompiled and sampling cost is negligible next to prefill for this study.
export VLLM_USE_FLASHINFER_SAMPLER="${VLLM_USE_FLASHINFER_SAMPLER:-0}"

# Development mode exposes POST /reset_prefix_cache, which benchmarks use to
# start every trial from an identical, empty cache. Replicas bind to all
# interfaces for WSL port forwarding; do not expose them beyond this machine.
export VLLM_SERVER_DEV_MODE="${VLLM_SERVER_DEV_MODE:-1}"

mkdir -p "$RUN_DIR"

port_of() { echo $((BASE_PORT + $1)); }

wait_healthy() {
  local port=$1 pid=$2 deadline=$((SECONDS + STARTUP_TIMEOUT_S))
  until curl -fsS "http://127.0.0.1:${port}/health" >/dev/null 2>&1; do
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "replica on port ${port} exited during startup; see ${RUN_DIR}/${port}.log" >&2
      rm -f "$RUN_DIR/$port.pid"
      return 1
    fi
    if ((SECONDS >= deadline)); then
      echo "replica on port ${port} not healthy after ${STARTUP_TIMEOUT_S}s; see ${RUN_DIR}/${port}.log" >&2
      return 1
    fi
    sleep 2
  done
}

start() {
  # shellcheck source=/dev/null
  source "$VENV_DIR/bin/activate"
  for ((i = 0; i < REPLICAS; i++)); do
    local port
    port=$(port_of "$i")
    if [[ -f "$RUN_DIR/$port.pid" ]] && kill -0 "$(cat "$RUN_DIR/$port.pid")" 2>/dev/null; then
      echo "replica $i already running on port $port"
      continue
    fi
    echo "starting replica $i on port $port"
    # setsid detaches the server from this shell's session, so it keeps running
    # after the invoking wsl.exe process exits.
    setsid nohup vllm serve "$MODEL" \
      --host 0.0.0.0 \
      --port "$port" \
      --kv-cache-memory-bytes "$KV_CACHE_BYTES" \
      --max-model-len "$MAX_MODEL_LEN" \
      --enable-prefix-caching \
      >"$RUN_DIR/$port.log" 2>&1 </dev/null &
    local pid=$!
    echo "$pid" >"$RUN_DIR/$port.pid"
    wait_healthy "$port" "$pid"
    echo "replica $i healthy"
  done
}

stop() {
  for ((i = 0; i < REPLICAS; i++)); do
    local port pidfile
    port=$(port_of "$i")
    pidfile="$RUN_DIR/$port.pid"
    if [[ -f "$pidfile" ]]; then
      # Signal the whole process group: vLLM runs its engine in a child process.
      kill -- "-$(cat "$pidfile")" 2>/dev/null || true
      rm -f "$pidfile"
      echo "stopped replica on port $port"
    fi
  done
}

status() {
  for ((i = 0; i < REPLICAS; i++)); do
    local port
    port=$(port_of "$i")
    if curl -fsS "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
      echo "port $port: healthy"
    else
      echo "port $port: down"
    fi
  done
}

logs() {
  local lines=${1:-20}
  for ((i = 0; i < REPLICAS; i++)); do
    local port
    port=$(port_of "$i")
    echo "== port $port (${RUN_DIR}/${port}.log)"
    tail -n "$lines" "$RUN_DIR/$port.log" 2>/dev/null || echo "(no log)"
  done
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  status) status ;;
  logs) logs "${2:-20}" ;;
  *)
    echo "usage: $0 start|stop|status|logs [lines]" >&2
    exit 2
    ;;
esac
