#!/usr/bin/env bash
#
# occ dev connect — local k3d tryout launcher.
#
# One command: resolve the workload's declared dependencies against the control
# plane, tunnel each to a local 127.0.0.1:<port>, and drop you into a subshell
# whose environment points at those listeners — so the app runs locally against
# the environment's real upstreams.
#
# It also performs the LOCAL-ONLY scaffolding that the real (LoadBalancer)
# deployment makes unnecessary, so you get the true one-command experience here:
#   1. builds occ from source          — the released binary predates `dev connect`;
#   2. labels the dev-agent            — openchoreo.dev/system-component, so the
#                                         workload's ingress NetworkPolicy admits it
#                                         (worklog Q5); resource services are unaffected;
#   3. port-forwards the dev-agent     — to the exact address the CP hands occ,
#                                         and tears it down on exit.
#
# In production none of (1)-(3) exist: occ dials the agent's own private
# LoadBalancer, wired into the CP response by the Helm chart (worklog §10.4).
#
# Usage:
#   hack/dev-connect/connect.sh                 # subshell against the doclet sample
#   hack/dev-connect/connect.sh --print-env     # print bindings, hold tunnels, no subshell
#   WORKLOAD=path/to/workload.yaml ENVIRONMENT=development hack/dev-connect/connect.sh
#
# Env overrides: WORKLOAD, ENVIRONMENT, NAMESPACE, OCC_BIN, CP_NS, DP_NS,
#                AGENT_DEPLOY, AGENT_SVC. Extra args pass through to occ dev connect.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/../..")"
ROOT="$(cd "$ROOT" && pwd)"
cd "$ROOT"

CP_NS="${CP_NS:-openchoreo-control-plane}"
DP_NS="${DP_NS:-openchoreo-data-plane}"
AGENT_DEPLOY="${AGENT_DEPLOY:-dev-agent}"
AGENT_SVC="${AGENT_SVC:-dev-agent}"
OCC_BIN="${OCC_BIN:-$ROOT/bin/occ-dev}"
WORKLOAD="${WORKLOAD:-samples/from-image/doclet/components/service-document.yaml}"
ENVIRONMENT="${ENVIRONMENT:-development}"
NAMESPACE="${NAMESPACE:-default}"

log()  { printf '\033[36m▸ %s\033[0m\n' "$*" >&2; }
warn() { printf '\033[33m! %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# 1 — occ binary that actually has `dev connect`.
if ! "$OCC_BIN" dev connect --help >/dev/null 2>&1; then
  log "building occ → $OCC_BIN (released binary lacks 'dev connect')"
  go build -o "$OCC_BIN" ./cmd/occ
fi

# 2 — dev-agent must be admitted by the workload's ingress NetworkPolicy (Q5).
label="$(kubectl -n "$DP_NS" get deploy "$AGENT_DEPLOY" \
  -o jsonpath='{.spec.template.metadata.labels.openchoreo\.dev/system-component}' 2>/dev/null || true)"
if [ -z "$label" ]; then
  log "labeling $AGENT_DEPLOY openchoreo.dev/system-component (Q5: admit to component-endpoint ingress)"
  kubectl -n "$DP_NS" patch deploy "$AGENT_DEPLOY" --type=merge \
    -p '{"spec":{"template":{"metadata":{"labels":{"openchoreo.dev/system-component":"dev-agent"}}}}}' >/dev/null
  kubectl -n "$DP_NS" rollout status deploy "$AGENT_DEPLOY" --timeout=120s >&2
fi

# 3 — discover the address the CP will hand occ; forward it only if it's local.
endpoint="$(kubectl -n "$CP_NS" get configmap openchoreo-api-config -o jsonpath='{.data.config\.yaml}' 2>/dev/null \
  | awk '/^[[:space:]]*agent_endpoint:/ {print $2; exit}')"
[ -n "$endpoint" ] || die "dev_connect.agent_endpoint not found in $CP_NS/openchoreo-api-config (is dev_connect enabled?)"
host="${endpoint%:*}"; port="${endpoint##*:}"
log "CP will route occ to agent endpoint: $endpoint"

pf_pid=""
cleanup() { [ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Genuine path first: if the advertised endpoint is already reachable (a real
# LoadBalancer, or the k3d loadbalancer mapping from hack/dev-connect/expose-agent.sh),
# occ dials it directly — no port-forward. Only fall back to a forward for a local
# endpoint that isn't yet routed.
if nc -z "$host" "$port" 2>/dev/null; then
  log "agent endpoint $endpoint already reachable — dialing directly, no port-forward"
elif [ "$host" = "127.0.0.1" ] || [ "$host" = "localhost" ]; then
  log "port-forward svc/$AGENT_SVC → $host:$port  (fallback scaffold — see expose-agent.sh for the direct path)"
  kubectl -n "$DP_NS" port-forward "svc/$AGENT_SVC" "${port}:8443" >/tmp/dev-agent-pf.log 2>&1 &
  pf_pid=$!
  for _ in $(seq 1 30); do nc -z "$host" "$port" 2>/dev/null && break; sleep 0.3; done
  nc -z "$host" "$port" 2>/dev/null || die "port-forward to :$port never became ready (see /tmp/dev-agent-pf.log)"
else
  die "agent endpoint $endpoint is not reachable (non-local, no route)"
fi

# 4 — the actual command. Extra args ($@) pass through, e.g. --print-env.
echo >&2
log "occ dev connect --workload $WORKLOAD --namespace $NAMESPACE --env $ENVIRONMENT $*"
"$OCC_BIN" dev connect --workload "$WORKLOAD" --namespace "$NAMESPACE" --env "$ENVIRONMENT" "$@"
