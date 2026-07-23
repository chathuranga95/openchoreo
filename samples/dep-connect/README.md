# `dep-connect` — samples for the `occ local` inner loop

Samples for exercising `occ local` (see `worklog.md`), which tunnels a workload's
declared dependencies to `127.0.0.1:<port>` and drops you in a subshell with the
matching env vars set. Two scenarios live here:

- **`external-postgres/`** — a **resource dependency**: a pre-existing Postgres
  wrapped as an OpenChoreo `Resource`, consumed by `workloads/database-test-workload.yaml`.
  Documented in full below.
- **`workloads/service-test-workload.yaml`** — an **endpoint dependency** fixture: a
  service that consumes other components' endpoints (including `database-test`'s), used
  to exercise multi-workload cross-linking. `hack/dep-connect/connect.sh` runs both
  workloads together by default.

## `external-postgres` — a pre-existing Postgres as an OpenChoreo Resource

A minimal, standalone sample for testing `occ local` (see `worklog.md`) against
a Postgres server that already exists somewhere reachable from the data plane's
network -- e.g. a container on the k3d docker network, a VM, or a managed database --
rather than one OpenChoreo provisions itself (contrast with
`samples/from-image/doclet/resources/postgres.yaml`, which provisions an in-cluster
StatefulSet).

It's owned by the `default` project so it can be consumed as a resource dependency of
`database-test` -- resource dependencies must live in the **same project** as the
consuming Component (no cross-project support yet, see `WorkloadResourceDependency`'s
doc comment). `database-test` itself is declared with `owner.projectName: default`
in `samples/dep-connect/workloads/database-test-workload.yaml` for this same
reason -- no need to stand up a separate Project just to exercise this tunnel.

⚠️ **All outputs, including `password`, are plain non-secret values** (see the
ClusterResourceType's description). That's deliberate: `occ local` v1 doesn't
resolve secret-kind outputs yet (worklog D3/D12), so this lets you exercise a fully
authenticated connection through the tunnel today. Don't reuse this ResourceType for
anything but a throwaway local test database.

## Apply

```bash
kubectl apply -f samples/dep-connect/external-postgres/cluster-resource-type.yaml
kubectl apply -f samples/dep-connect/external-postgres/resource.yaml
kubectl apply -f samples/dep-connect/external-postgres/binding-development.yaml
```

## Promote

A Resource's binding stays pending until pinned to a specific `ResourceRelease`:

```bash
release=$(kubectl get resource external-postgres -n default -o jsonpath='{.status.latestRelease.name}')
kubectl patch resourcereleasebinding external-postgres-development -n default \
  --type=merge -p "{\"spec\":{\"resourceRelease\":\"$release\"}}"
```

Wait for it to reach `Ready=True`:

```bash
kubectl get resourcereleasebinding external-postgres-development -n default
```

## Consume it as a dependency

`samples/dep-connect/workloads/database-test-workload.yaml` already declares it:

```yaml
dependencies:
  resources:
    - ref: external-postgres
      envBindings:
        host: DB_HOST
        port: DB_PORT
        database: DB_NAME
        username: DB_USER
        password: DB_PASSWORD
```

Any other Component in the **`default` project** could reference it the same way; a
Component in a different project cannot (see the note above).

Then:

```bash
occ local samples/dep-connect/workloads/database-test-workload.yaml \
  --namespace default --env development
```

tunnels to `172.18.0.4:5432` and hands you a working `$DB_PASSWORD` too.
