#!/usr/bin/env bash
#
# occ local — local k3d tryout launcher.
#
# One command: resolve each given workload's declared dependencies against the
# control plane, tunnel each remote one to a local 127.0.0.1:<port> (dependencies on
# another of the given workloads are wired straight to a local host:port instead —
# see occ local --help), and drop you into a subshell whose environment points at
# those listeners — so the app runs locally against the environment's real
# upstreams.
#
# The tunnel rides through openchoreo-api and the existing cluster-gateway /
# cluster-agent management tunnel (worklog §8) — the same path `occ exec` already
# uses. There is no separate dev-tunnel agent or endpoint to expose, so the only
# local-only scaffolding needed here is building occ from source, since the
# released binary predates `occ local`.
#
# Usage:
#   hack/dep-connect/connect.sh                 # subshell against the dep-connect test-workload sample
#   hack/dep-connect/connect.sh --print-env     # print bindings, hold tunnels, no subshell
#   WORKLOADS="comp1/workload.yaml comp2/workload.yaml" ENVIRONMENT=development hack/dep-connect/connect.sh
#
# Env overrides: DATABASE_WORKLOAD, SERVICE_WORKLOAD, WORKLOADS (space-separated,
# overrides both of the above), ENVIRONMENT, NAMESPACE, OCC_BIN.
# Extra args pass through to occ local (e.g. --local comp2=127.0.0.1:9091).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/../..")"
ROOT="$(cd "$ROOT" && pwd)"
cd "$ROOT"

OCC_BIN="${OCC_BIN:-$ROOT/bin/occ-dev}"
DATABASE_WORKLOAD="${DATABASE_WORKLOAD:-samples/dep-connect/workloads/database-test-workload.yaml}"
SERVICE_WORKLOAD="${SERVICE_WORKLOAD:-samples/dep-connect/workloads/service-test-workload.yaml}"
WORKLOADS="${WORKLOADS:-$DATABASE_WORKLOAD $SERVICE_WORKLOAD}"
ENVIRONMENT="${ENVIRONMENT:-development}"
NAMESPACE="${NAMESPACE:-default}"
# shellcheck disable=SC2206 # intentional word-splitting: WORKLOADS is a space-separated list of paths
WORKLOAD_ARGS=($WORKLOADS)

log()  { printf '\033[36m▸ %s\033[0m\n' "$*" >&2; }

# occ binary that actually has `local`.
if ! "$OCC_BIN" local --help >/dev/null 2>&1; then
  log "building occ → $OCC_BIN (released binary lacks 'local')"
  go build -o "$OCC_BIN" ./cmd/occ
fi

echo >&2
log "occ local ${WORKLOAD_ARGS[*]} --namespace $NAMESPACE --env $ENVIRONMENT $*"
"$OCC_BIN" local "${WORKLOAD_ARGS[@]}" --namespace "$NAMESPACE" --env "$ENVIRONMENT" "$@"
