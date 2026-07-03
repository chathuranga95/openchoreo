# `occ dev connect` — local k3d tryout

Gives you the real `occ dev connect` inner loop on a local k3d cluster: land in a
subshell where the workload's dependencies are reachable at `127.0.0.1:<port>` and
the matching env vars are set.

```bash
# one-time: expose the dev-agent so occ dials it directly (no port-forward)
hack/dev-connect/expose-agent.sh

# then, any time:
hack/dev-connect/connect.sh
```

Inside the subshell:

```bash
echo "$GREETER_SERVICE_URL"          # http://127.0.0.1:<port>
curl "$GREETER_SERVICE_URL/greeter/greet?name=OpenChoreo"
echo "$DB_HOST $DB_PORT"             # 127.0.0.1 <port>
nc -z "$DB_HOST" "$DB_PORT" && echo "postgres reachable through the tunnel"
exit                                 # tears everything down
```

## The direct path — `expose-agent.sh`

This is the genuine experience: occ dials the address the control plane advertises
(`dev_connect.agent_endpoint`) **directly**, exactly as it would hit a production
LoadBalancer. The script wires that on k3d and is idempotent:

- dev-agent `Service` → `LoadBalancer` on the advertised port (targetPort 8443);
- adds a k3d loadbalancer host mapping so `127.0.0.1:<port>` routes to it
  (`laptop → serverlb → node hostPort → svclb → dev-agent`);
- verifies the laptop reaches the agent's TLS listener.

Re-run it after a cluster rebuild. Undo with `hack/dev-connect/expose-agent.sh --revert`.
Adding the host mapping briefly recreates the k3d serverlb, so `*.localhost` ingress
blips for a few seconds (workload pods are untouched).

## What `connect.sh` does

| Step | Why (local only) |
|------|------------------|
| `go build -o bin/occ-dev ./cmd/occ` | the released `occ` predates `dev connect` |
| labels the `dev-agent` deployment `openchoreo.dev/system-component` | the workload's ingress NetworkPolicy admits system components; without it, endpoint tunnels get `connection refused` (worklog **Q5**). Resource tunnels (Postgres) are unaffected |
| reaches the agent | **probe-first**: if the advertised endpoint is already reachable (after `expose-agent.sh`, or a real LoadBalancer) it dials directly; otherwise it falls back to a `kubectl port-forward` |

In production none of this exists: the Helm chart ships the dev-agent behind its own
LoadBalancer and labels it, so a developer runs **only** `occ dev connect` — no build,
no port-forward, no cluster access (just `occ login`). See worklog §10.4.

## Options

```bash
hack/dev-connect/connect.sh --print-env          # print bindings + hold tunnels, no subshell
WORKLOAD=path/to/workload.yaml   \
ENVIRONMENT=development NAMESPACE=default \
  hack/dev-connect/connect.sh
```

Env overrides: `WORKLOAD`, `ENVIRONMENT`, `NAMESPACE`, `OCC_BIN`, `CP_NS`, `DP_NS`,
`AGENT_DEPLOY`, `AGENT_SVC`. Any extra args pass through to `occ dev connect`.
`expose-agent.sh` also honors `CLUSTER` and `PORT`.

## Prerequisites

- k3d cluster `openchoreo` up, with the dev-connect components deployed and
  `dev_connect.enabled: true` in `openchoreo-api-config` (worklog §10.3 B1–B5).
- Logged in: `occ login`.
- The doclet sample + `greeter-service` / `h2-greeter` deployed and `Ready`
  (the default `WORKLOAD` depends on them).

## Reverting the local mutations

```bash
hack/dev-connect/expose-agent.sh --revert     # dev-agent Service back to ClusterIP
# drop the Q5 label:
kubectl -n openchoreo-data-plane patch deploy dev-agent --type=json \
  -p '[{"op":"remove","path":"/spec/template/metadata/labels/openchoreo.dev~1system-component"}]'
```
