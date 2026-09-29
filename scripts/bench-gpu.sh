#!/usr/bin/env bash
# Runs a workload through the router against the local vLLM replicas, once
# per policy per trial, and writes a comparison table. Every trial starts with
# every replica's prefix cache reset, and the router is restarted so no
# learned state carries over between trials.
#
# Usage:
#   scripts/bench-gpu.sh OUT_DIR "POLICY[:precise]..." TRIALS [loadgen run flags...]
#
# A ":precise" suffix runs that policy in precise prefix mode, driven by the
# replicas' KV cache events (replica N publishes on port KV_EVENTS_BASE_PORT+N).
#
# Example:
#   scripts/bench-gpu.sh bench/results/gpu-agent "round_robin estimated_ttft" 3 \
#     -workload agent -duration 5m -rate 0.5
#
# Requires the replicas from `make vllm-up` (see deploy/vllm/README.md).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

if (($# < 3)); then
  echo "usage: $0 OUT_DIR \"POLICY...\" TRIALS [loadgen run flags...]" >&2
  exit 2
fi
out_dir=$1 policies=$2 trials=$3
shift 3

EXE=""
[[ "${OS:-}" == "Windows_NT" ]] && EXE=".exe"
MODEL=${MODEL:-Qwen/Qwen2.5-1.5B-Instruct}
PORT=${PORT:-8090}
BACKENDS=${BACKENDS:-http://localhost:8001,http://localhost:8002,http://localhost:8003,http://localhost:8004}
KV_EVENTS_BASE_PORT=${KV_EVENTS_BASE_PORT:-5601}
run_dir=.run/gpu-bench
mkdir -p "$out_dir" "$run_dir"

router_pid=""
stop_router() {
  if [[ -n "$router_pid" ]]; then
    kill "$router_pid" 2>/dev/null || true
    wait "$router_pid" 2>/dev/null || true
    router_pid=""
  fi
}
trap stop_router EXIT

# Routing parameters match the reference setup (ADR 0009): 37,440 tokens of
# KV cache per replica; the synthetic text is 5.55 bytes per token.
write_config() {
  local policy=$1 mode=$2 config=$3
  {
    echo "server:"
    echo "  listen: \":$PORT\""
    echo "  explain_headers: true"
    echo "routing:"
    echo "  policy: $policy"
    echo "  prefix_mode: $mode"
    echo "  bytes_per_token: 5.55"
    echo "  kv_capacity_tokens: 37440"
    echo "log:"
    echo "  level: warn"
    echo "backends:"
    local i=0
    for url in ${BACKENDS//,/ }; do
      printf '  - id: vllm-%d\n    url: %s\n' "$i" "$url"
      if [[ "$mode" == precise ]]; then
        printf '    kv_events_endpoint: tcp://localhost:%d\n' $((KV_EVENTS_BASE_PORT + i))
      fi
      i=$((i + 1))
    done
  } >"$config"
}

for ((trial = 1; trial <= trials; trial++)); do
  for spec in $policies; do
    policy=${spec%%:*}
    mode=approximate
    [[ "$spec" == *:precise ]] && mode=precise
    name=$policy
    [[ "$mode" == precise ]] && name="$policy-precise"
    config="$run_dir/$name.yaml"
    write_config "$policy" "$mode" "$config"
    "bin/switchyard$EXE" -config "$config" >"$run_dir/$name-$trial.router.log" 2>&1 &
    router_pid=$!
    until curl -fsS "http://localhost:$PORT/readyz" >/dev/null 2>&1; do sleep 0.3; done

    "bin/loadgen$EXE" run \
      -url "http://localhost:$PORT" \
      -model "$MODEL" \
      -backends "$BACKENDS" \
      -reset-caches \
      -label "$name (trial $trial)" \
      -out "$out_dir/$name-trial$trial.jsonl" \
      "$@"
    stop_router
  done
done

{
  echo "# GPU benchmark"
  echo
  echo "Generated $(date -u +%Y-%m-%dT%H:%MZ) by \`scripts/bench-gpu.sh $out_dir \"$policies\" $trials $*\`."
  echo "Four vLLM replicas of $MODEL on one RTX 4090 (see deploy/vllm/README.md and ADR 0009)."
  echo "Replicas share one GPU; results compare policies under identical conditions."
  echo
  "bin/loadgen$EXE" report -group "$out_dir"/*.jsonl
} >"$out_dir/summary.md"
cat "$out_dir/summary.md"
