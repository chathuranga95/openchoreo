#!/usr/bin/env bash
#
# Wire the "genuine path" for occ dev connect on a local k3d cluster: expose the
# dev-agent so occ dials the address the control plane advertises DIRECTLY, with no
# kubectl port-forward. This mirrors what the production Helm chart's dedicated
# LoadBalancer does (worklog §10.4 / D8).
#
# Path once wired:
#   laptop 127.0.0.1:PORT
#     -> k3d loadbalancer (serverlb) host mapping  PORT:PORT
#     -> node hostPort PORT  (klipper svclb for the dev-agent LoadBalancer Service)
#     -> dev-agent Service -> dev-agent pod :8443
#
# Usage:
#   hack/dev-connect/expose-agent.sh            # wire it (idempotent)
#   hack/dev-connect/expose-agent.sh --revert   # dev-agent Service back to ClusterIP
#
# Env overrides: CLUSTER (k3d cluster name), DP_NS, CP_NS, AGENT_SVC, PORT.
set -euo pipefail

CLUSTER="${CLUSTER:-openchoreo}"
DP_NS="${DP_NS:-openchoreo-data-plane}"
CP_NS="${CP_NS:-openchoreo-control-plane}"
AGENT_SVC="${AGENT_SVC:-dev-agent}"

log()  { printf '\033[36m▸ %s\033[0m\n' "$*" >&2; }
warn() { printf '\033[33m! %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# The port occ will dial = dev_connect.agent_endpoint's port, so the wiring and the
# control plane can't drift. Override with PORT=.
endpoint="$(kubectl -n "$CP_NS" get configmap openchoreo-api-config -o jsonpath='{.data.config\.yaml}' 2>/dev/null \
  | awk '/^[[:space:]]*agent_endpoint:/ {print $2; exit}')"
host="${endpoint%:*}"
PORT="${PORT:-${endpoint##*:}}"
[ -n "$PORT" ] || die "could not determine PORT (set PORT= or dev_connect.agent_endpoint)"
case "${host:-127.0.0.1}" in
  127.0.0.1|localhost|"") : ;;
  *) warn "dev_connect.agent_endpoint host is '$host' (not local) — this script targets local k3d";;
esac

if [ "${1:-}" = "--revert" ]; then
  log "reverting dev-agent Service to ClusterIP"
  kubectl -n "$DP_NS" patch svc "$AGENT_SVC" --type merge \
    -p '{"spec":{"type":"ClusterIP","ports":[{"name":"tunnel","port":8443,"targetPort":8443,"protocol":"TCP"}]}}'
  warn "k3d has no single-port removal; the host:$PORT serverlb mapping stays (harmless/dangling)."
  warn "to drop it, recreate the cluster or 'k3d cluster edit' without it."
  exit 0
fi

# 1 - dev-agent as LoadBalancer PORT -> 8443 (idempotent)
cur_type="$(kubectl -n "$DP_NS" get svc "$AGENT_SVC" -o jsonpath='{.spec.type}' 2>/dev/null || true)"
cur_port="$(kubectl -n "$DP_NS" get svc "$AGENT_SVC" -o jsonpath='{.spec.ports[0].port}' 2>/dev/null || true)"
if [ "$cur_type" = "LoadBalancer" ] && [ "$cur_port" = "$PORT" ]; then
  log "dev-agent Service already LoadBalancer:$PORT"
else
  log "exposing dev-agent Service as LoadBalancer:$PORT -> 8443"
  kubectl -n "$DP_NS" patch svc "$AGENT_SVC" --type merge \
    -p "{\"spec\":{\"type\":\"LoadBalancer\",\"ports\":[{\"name\":\"tunnel\",\"port\":$PORT,\"targetPort\":8443,\"protocol\":\"TCP\"}]}}"
fi

# 2 - k3d serverlb host mapping PORT:PORT (idempotent; recreates only the serverlb)
if docker ps --filter "name=k3d-${CLUSTER}-serverlb" --format '{{.Ports}}' 2>/dev/null | grep -q ":${PORT}->"; then
  log "k3d host mapping :$PORT already present"
else
  warn "adding k3d host mapping ${PORT}:${PORT}@loadbalancer — recreates the serverlb; *.localhost ingress blips briefly"
  k3d cluster edit "$CLUSTER" --port-add "${PORT}:${PORT}@loadbalancer"
fi

# 3 - verify the laptop reaches the agent's TLS listener with no port-forward
log "verifying 127.0.0.1:$PORT reaches the dev-agent..."
subj=""
for _ in $(seq 1 30); do
  subj="$(echo | openssl s_client -connect "127.0.0.1:${PORT}" 2>/dev/null | openssl x509 -noout -subject 2>/dev/null || true)"
  [ -n "$subj" ] && break
  sleep 1
done
[ -n "$subj" ] || die "127.0.0.1:$PORT not reachable — check the svclb-$AGENT_SVC pod and the serverlb container"
log "OK — $subj reachable at 127.0.0.1:$PORT (no port-forward)"
echo >&2
log "occ now dials the agent directly. Run:  hack/dev-connect/connect.sh"
