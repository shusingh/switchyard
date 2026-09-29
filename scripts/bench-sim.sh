#!/usr/bin/env bash
# Runs one workload against a fresh simulated cluster per routing policy and
# writes a comparison table. Every policy sees the identical seeded workload
# and starts with empty caches.
#
# Usage:
#   scripts/bench-sim.sh OUT_DIR REPLICAS "POLICY..." [loadgen run flags...]
#
# Example:
#   scripts/bench-sim.sh bench/results/sim-agent-4 4 "random round_robin least_loaded p2c" \
#     -workload agent -duration 5m -rate 0.5
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

if (($# < 3)); then
  echo "usage: $0 OUT_DIR REPLICAS \"POLICY...\" [loadgen run flags...]" >&2
  exit 2
fi
out_dir=$1 replicas=$2 policies=$3
shift 3

EXE=""
[[ "${OS:-}" == "Windows_NT" ]] && EXE=".exe"
mkdir -p "$out_dir"
backends=$(scripts/sim-cluster.sh backends "$replicas")
trap 'scripts/sim-cluster.sh stop >/dev/null 2>&1 || true' EXIT

for policy in $policies; do
  scripts/sim-cluster.sh start "$replicas" "$policy"
  "bin/loadgen$EXE" run \
    -url "http://localhost:${ROUTER_PORT:-8080}" \
    -model sim-model \
    -backends "$backends" \
    -label "$policy ($replicas replicas, simulated)" \
    -out "$out_dir/$policy.jsonl" \
    "$@"
  scripts/sim-cluster.sh stop
done

{
  echo "# Simulated benchmark: $replicas replicas"
  echo
  echo "Generated $(date -u +%Y-%m-%dT%H:%MZ) by \`scripts/bench-sim.sh $out_dir $replicas \"$policies\" $*\`."
  echo "Simulated engines (internal/sim, default cost model); not GPU measurements."
  echo
  "bin/loadgen$EXE" report "$out_dir"/*.jsonl
} >"$out_dir/summary.md"
cat "$out_dir/summary.md"
