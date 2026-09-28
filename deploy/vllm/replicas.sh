#!/usr/bin/env bash
# Starts, stops, or reports on N vLLM replicas sharing one GPU.
#
# Usage: replicas.sh start|stop|status
#
# Replicas start one at a time: vLLM profiles free GPU memory at startup, so
# concurrent startups can each see memory the others are about to claim.
# Each replica gets a small, fixed share of GPU memory, which also caps its
# KV cache. That is deliberate: prefix-aware routing only matters when the hot
# working set exceeds one replica's cache (see DESIGN.md section 14.3).
set -euo pipefail

VENV_DIR="${VENV_DIR:-$HOME/venvs/vllm}"
MODEL="${MODEL:-Qwen/Qwen2.5-1.5B-Instruct}"
REPLICAS="${REPLICAS:-4}"
BASE_PORT="${BASE_PORT:-8001}"
GPU_MEM_FRACTION="${GPU_MEM_FRACTION:-0.2}"
MAX_MODEL_LEN="${MAX_MODEL_LEN:-16384}"
STARTUP_TIMEOUT_S="${STARTUP_TIMEOUT_S:-600}"
RUN_DIR="${RUN_DIR:-$HOME/.switchyard/vllm}"

mkdir -p "$RUN_DIR"

port_of() { echo $((BASE_PORT + $1)); }

wait_healthy() {
  local port=$1 deadline=$((SECONDS + STARTUP_TIMEOUT_S))
  until curl -fsS "http://127.0.0.1:${port}/health" >/dev/null 2>&1; do
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
    nohup vllm serve "$MODEL" \
      --host 0.0.0.0 \
      --port "$port" \
      --gpu-memory-utilization "$GPU_MEM_FRACTION" \
      --max-model-len "$MAX_MODEL_LEN" \
      --enable-prefix-caching \
      >"$RUN_DIR/$port.log" 2>&1 &
    echo $! >"$RUN_DIR/$port.pid"
    wait_healthy "$port"
    echo "replica $i healthy"
  done
}

stop() {
  for ((i = 0; i < REPLICAS; i++)); do
    local port pidfile
    port=$(port_of "$i")
    pidfile="$RUN_DIR/$port.pid"
    if [[ -f "$pidfile" ]]; then
      kill "$(cat "$pidfile")" 2>/dev/null || true
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

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  status) status ;;
  *)
    echo "usage: $0 start|stop|status" >&2
    exit 2
    ;;
esac
