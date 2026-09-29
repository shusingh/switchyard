#!/usr/bin/env bash
# Starts or stops a local simulated cluster: N simengine replicas behind one
# switchyard router. Every start creates fresh engines, so caches begin empty.
#
# Usage:
#   scripts/sim-cluster.sh start [replicas] [policy]
#   scripts/sim-cluster.sh stop
#
# Environment: ROUTER_PORT (8080), BASE_PORT (9001), SIM_FLAGS (extra
# simengine flags), ROUTING_EXTRA (extra YAML lines under routing, such as
# "  block_bytes: 64"), SIM_RUN_DIR (.run/sim; use a separate one per
# concurrent cluster), BIN_DIR (./bin). Run `make build` first.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

RUN_DIR=${SIM_RUN_DIR:-.run/sim}
BIN_DIR=${BIN_DIR:-bin}
ROUTER_PORT=${ROUTER_PORT:-8080}
BASE_PORT=${BASE_PORT:-9001}
EXE=""
[[ "${OS:-}" == "Windows_NT" ]] && EXE=".exe"

wait_ready() {
  local url=$1 deadline=$((SECONDS + 30))
  until curl -fsS "$url" >/dev/null 2>&1; do
    if ((SECONDS >= deadline)); then
      echo "not ready after 30s: $url" >&2
      return 1
    fi
    sleep 0.2
  done
}

start() {
  local replicas=${1:-4} policy=${2:-round_robin}
  stop >/dev/null 2>&1 || true
  mkdir -p "$RUN_DIR"

  local config="$RUN_DIR/router.yaml"
  # Routing parameters match the simulated engines' defaults (sim.CostModel):
  # 2,340 blocks of 16 tokens, 5.55 bytes per token, 14,000 prefill tokens per
  # second. Explain headers let loadgen measure prediction error.
  {
    echo "server:"
    echo "  listen: \":$ROUTER_PORT\""
    echo "  explain_headers: true"
    echo "routing:"
    echo "  policy: $policy"
    echo "  bytes_per_token: 5.55"
    echo "  kv_capacity_tokens: 37440"
    echo "  prefill_tokens_per_second: 14000"
    if [[ -n "${ROUTING_EXTRA:-}" ]]; then
      echo "$ROUTING_EXTRA"
    fi
    echo "log:"
    echo "  level: warn"
    echo "backends:"
  } >"$config"

  for ((i = 0; i < replicas; i++)); do
    local port=$((BASE_PORT + i))
    # shellcheck disable=SC2086 # SIM_FLAGS is intentionally word-split
    "$BIN_DIR/simengine$EXE" -listen ":$port" ${SIM_FLAGS:-} >"$RUN_DIR/sim-$port.log" 2>&1 &
    echo $! >"$RUN_DIR/sim-$port.pid"
    printf '  - id: sim-%d\n    url: http://localhost:%d\n' "$i" "$port" >>"$config"
  done
  for ((i = 0; i < replicas; i++)); do
    wait_ready "http://localhost:$((BASE_PORT + i))/health"
  done

  "$BIN_DIR/switchyard$EXE" -config "$config" >"$RUN_DIR/router.log" 2>&1 &
  echo $! >"$RUN_DIR/router.pid"
  wait_ready "http://localhost:$ROUTER_PORT/readyz"
  echo "simulated cluster up: $replicas replicas, policy $policy, router on :$ROUTER_PORT"
}

stop() {
  shopt -s nullglob
  for pidfile in "$RUN_DIR"/*.pid; do
    kill "$(cat "$pidfile")" 2>/dev/null || true
    rm -f "$pidfile"
  done
  echo "simulated cluster stopped"
}

# backends prints the replica URLs as a comma-separated list, for
# loadgen -backends.
backends() {
  local replicas=${1:-4} urls=()
  for ((i = 0; i < replicas; i++)); do
    urls+=("http://localhost:$((BASE_PORT + i))")
  done
  (IFS=,; echo "${urls[*]}")
}

case "${1:-}" in
  start) start "${2:-4}" "${3:-round_robin}" ;;
  stop) stop ;;
  backends) backends "${2:-4}" ;;
  *)
    echo "usage: $0 start [replicas] [policy] | stop | backends [replicas]" >&2
    exit 2
    ;;
esac
