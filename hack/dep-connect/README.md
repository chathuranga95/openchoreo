# `occ local` — local k3d tryout

Gives you the real `occ local` inner loop on a local k3d cluster: land in a subshell where the workload's dependencies are reachable at `127.0.0.1:<port>` and the matching env vars are set.

```bash
hack/dep-connect/connect.sh
```

Inside the subshell:

```bash
echo "$GREETER_SERVICE_URL"          # http://127.0.0.1:<port>
curl "$GREETER_SERVICE_URL/greeter/greet?name=OpenChoreo"
echo "$DB_HOST $DB_PORT"             # 127.0.0.1 <port>
nc -z "$DB_HOST" "$DB_PORT" && echo "postgres reachable through the tunnel"
exit                                 # tears everything down
```

## What `connect.sh` does

The tunnel rides through `openchoreo-api` and the existing cluster-gateway / cluster-agent management tunnel (worklog §8) — the same path `occ exec` already uses. There is no separate dev-tunnel agent or endpoint to expose or wire up, so `connect.sh` only builds `occ` from source (the released binary predates `occ local`) and then runs `occ local` — the same one command a real deployment gives you.

## Options

```bash
hack/dep-connect/connect.sh --print-env          # print bindings + hold tunnels, no subshell
WORKLOADS="comp1/workload.yaml comp2/workload.yaml" \
ENVIRONMENT=development NAMESPACE=default \
  hack/dep-connect/connect.sh
```

`WORKLOADS` is a space-separated list — pass more than one to test dependencies between components you're running locally yourself (`occ local`'s cross-linking, see `occ local --help`). Env overrides: `WORKLOADS`, `ENVIRONMENT`, `NAMESPACE`, `OCC_BIN`. Any extra args pass through to `occ local` (e.g. `--local comp2=127.0.0.1:9091`).

## Prerequisites

- k3d cluster `openchoreo` up (`install-openchoreo-k3d`), `occ login` done.
- The doclet sample + `greeter-service` / `h2-greeter` deployed and `Ready` (the default `WORKLOADS` depends on them).
- The cluster primed per **"Priming a fresh cluster"** below — `dep_connect` isn't enabled by default, and a cluster installed from the published Helm chart needs a few more one-off patches to match this branch's architecture. None of this is needed once these pieces are folded into the chart proper (see the "Durable fix" notes below).

## Priming a fresh cluster

These are live, reversible `kubectl` patches against the running cluster — nothing here is persisted by Helm, so redo them after any fresh install or full `helm upgrade`. All commands assume `kubectl` is pointed at `k3d-openchoreo`.

### 1. Enable `dep_connect` on `openchoreo-api`

The resolve/stream endpoints only register when `dep_connect.enabled: true` is set in `openchoreo-api`'s config (`internal/openchoreo-api/config/dep_connect.go` defaults it to `false`). Without this, `occ local` fails immediately with `resolve failed: 404 Not Found`.

```bash
# 1a. Generate a signing key for the capability JWTs
openssl genpkey -algorithm ed25519 -out /tmp/dep-connect-signing.pem

# 1b. Store it as a Secret and mount it into the openchoreo-api pod
kubectl create secret generic dep-connect-signing-key \
  -n openchoreo-control-plane \
  --from-file=signing.pem=/tmp/dep-connect-signing.pem

kubectl patch deployment openchoreo-api -n openchoreo-control-plane --type=json -p='[
  {"op": "add", "path": "/spec/template/spec/volumes/-",
   "value": {"name": "dep-connect-signing-key", "secret": {"secretName": "dep-connect-signing-key"}}},
  {"op": "add", "path": "/spec/template/spec/containers/0/volumeMounts/-",
   "value": {"name": "dep-connect-signing-key", "mountPath": "/etc/dep-connect", "readOnly": true}}
]'

# 1c. Add the dep_connect block to openchoreo-api-config (edit the ConfigMap's
# config.yaml and add, alongside the existing `cluster_gateway:` block):
#
#   dep_connect:
#     enabled: true
#     signing_key_path: "/etc/dep-connect/signing.pem"
#     key_id: "dep-connect-1"
#     issuer: "openchoreo-control-plane"
#     ttl_seconds: 1800
kubectl edit configmap openchoreo-api-config -n openchoreo-control-plane
```

**Durable fix:** template `dep_connect` (and the signing-key secret mount) into `install/helm/openchoreo-control-plane/templates/openchoreo-api/`.

### 2. Allow the `depconnect-tcp` upgrade through the local gateway

The kgateway/Envoy fronting `api.openchoreo.localhost:8080` only passes through upgrade protocols explicitly listed on an `HTTPListenerPolicy` — by default just `websocket` (needed for `occ exec` / wirelogs). The dep-connect tunnel uses a different `Upgrade: depconnect-tcp` header (`internal/depconnect/upgrade.go`), so without this it 403s before ever reaching `openchoreo-api` (symptom: connection resets on the local tunnel port, nothing logged anywhere since `occ`'s `forward()` loop swallows dial errors silently).

```bash
kubectl patch httplistenerpolicy enable-websocket -n openchoreo-control-plane \
  --type=json -p='[{"op":"add","path":"/spec/upgradeConfig/enabledUpgrades/-","value":"depconnect-tcp"}]'
```

If there's no `enable-websocket` `HTTPListenerPolicy` yet, create one targeting `Gateway/gateway-default` with `spec.upgradeConfig.enabledUpgrades: ["websocket", "depconnect-tcp"]` (see the `k3d-gateway-websocket-403` note this repeats — this is an install gap, not dep-connect-specific).

**Durable fix:** add this `HTTPListenerPolicy` to `install/helm/openchoreo-control-plane/`.

### 3. `cluster_gateway.url` must point at the internal port (`8444`)

`cluster-gateway` (`internal/cluster-gateway/server.go`) runs **two** listeners: a public one (`Port`, `/ws` only, for remote data planes) and an internal one (`InternalPort`, `/api/*` — proxy, exec, wirelogs, and `/api/depconnect/`, all in-cluster-caller-only). `openchoreo-api-config`'s `cluster_gateway.url` and `controller-manager`'s `--cluster-gateway-url`/`CLUSTER_GATEWAY_URL` must all point at the **internal** port:

```yaml
cluster_gateway:
  url: "https://cluster-gateway.openchoreo-control-plane.svc.cluster.local:8444"
```

As of `2b483da95` (rebase onto upstream/main, 2026-07-16) this branch's `cluster-gateway` matches this two-port design out of the box — a fresh install from the published chart already has `--internal-port=8444` on the Deployment and both Service ports, so nothing to patch here on a fresh cluster. This was only a real gotcha on a pre-rebase checkout of this branch, whose `cluster-gateway` briefly served everything on one port (`8443`) — rebuilding *that* code against a chart's two-port Deployment left port `8444` with nothing listening (`connection refused` from both `openchoreo-api` and `controller-manager`), and required stripping `--internal-port` from the Deployment args and pointing both configs at `:8443` instead. If you still see `dial cluster-gateway...:8444: connection refused` after rebuilding, first check you're not on a stale pre-rebase commit; if you deliberately reverted to single-port config earlier in a session, undo it — add `--internal-port=8444` back to the `cluster-gateway` Deployment args and point `cluster_gateway.url` (`openchoreo-api-config`) and `controller-manager`'s `--cluster-gateway-url`/`CLUSTER_GATEWAY_URL` back at `:8444`.

### 4. Rebuild and redeploy the components carrying dep-connect code

`openchoreo-api`, `cluster-gateway`, and `cluster-agent` all need code from this branch — a k3d cluster's images predate it until you push a build through:

```bash
make k3d.update.openchoreo-api
make k3d.update.cluster-gateway
make k3d.update.cluster-agent
```

### 5. Let the cluster-agent past per-component NetworkPolicies

Each deployed component gets a generated `NetworkPolicy` admitting ingress only from same-namespace pods or pods labeled `openchoreo.dev/system-component`. `cluster-agent` isn't labeled that way by default, so dialing an endpoint dependency (as opposed to a resource dependency) fails with `connection refused` even after everything else above is fixed.

```bash
kubectl label pod -n openchoreo-data-plane -l app=cluster-agent \
  openchoreo.dev/system-component=cluster-agent --overwrite
```

This is a live pod label (NetworkPolicy evaluates source labels in real time, no rollout needed) — it's lost on the next pod recreation, so redo it after any `k3d.update.cluster-agent` / rollout restart.

**Durable fix:** add `openchoreo.dev/system-component: cluster-agent` to the cluster-agent pod template in `install/helm/openchoreo-data-plane/templates/cluster-agent/deployment.yaml`.

## Testing a resource dependency (`external-postgres`)

The default `WORKLOADS` entry also declares a `resources` dependency on `external-postgres` (`samples/dep-connect/workloads/database-test-workload.yaml`), wired to `DB_HOST`/`DB_PORT`/`DB_USER`/`DB_PASSWORD`/`DB_NAME`. Unlike the endpoint deps, this needs its own `Resource` + `ResourceReleaseBinding` applied first — see `samples/dep-connect/README.md` for the full apply/promote steps. Everything in that sample and in `database-test-workload.yaml` is owned by the `default` project (no need to stand up a separate Project just for this). A resource dependency's `ResourceReleaseBinding` must live in the **same project** as the consuming Component's `owner.projectName` — a mismatch there (or a binding that references a project with no data-plane namespace yet, i.e. nothing else has ever deployed to that project+environment) surfaces as `no resource release binding for <project>/<ref> in <env>` from `occ local`.

## Troubleshooting

- **`resolve failed: 404 Not Found`** — `dep_connect.enabled` isn't set; see step 1.
- **Tunnel prints bindings but every connection resets immediately, nothing in any component's logs** — the gateway is 403ing the upgrade before it reaches `openchoreo-api`; see step 2.
- **`openchoreo-api` logs `TLS handshake with cluster-gateway...: either ServerName or InsecureSkipVerify must be specified`** — fixed in code (`internal/depconnect/upgrade.go`'s `DialUpgrade` now derives `ServerName` from the target host); rebuild `openchoreo-api` (step 4) if you still see this.
- **`openchoreo-api` logs `dial cluster-gateway...:8444: connection refused`** — either `cluster-gateway`'s internal listener isn't up yet (check its pod logs for `internal API server starting`) or you're on a stale pre-rebase checkout with the single-port design; see step 3.
- **`gateway rejected dep-connect dial ... dial failed: invalid exec path`** — `cluster-agent` is stale and is routing the "tcp" stream init through the exec fallback path; rebuild it (step 4).
- **`gateway rejected dep-connect dial ... dial tcp <ip>:<port>: connection refused`** (after step 4) — NetworkPolicy blocking the agent; see step 5.
- **`! <ref>: no resource release binding for <project>/<ref> in <env>`** — either no `ResourceReleaseBinding` exists yet for that (project, ref, env), or its `spec.owner.projectName` doesn't match the consuming Component's `owner.projectName`; see "Testing a resource dependency" above.
- **`ResourceReleaseBinding` stuck `Ready=False` with `dial tcp ...:8444: connection refused`** — `controller-manager` (not just `openchoreo-api`) needs `cluster_gateway`'s URL fixed too; see step 3.
- **`ResourceReleaseBinding` stuck `Ready=False` with `namespaces "dp-<ns>-<project>-<env>-<hash>" not found`** — nothing has ever been deployed to that project+environment yet, so its data-plane namespace doesn't exist. Deploy any `autoDeploy: true` Component into that project/environment first (or just use the `default` project, which likely already has one from other samples).
