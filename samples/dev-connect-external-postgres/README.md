# `external-postgres` — a pre-existing Postgres as an OpenChoreo Resource

A minimal, standalone sample for testing `occ dev connect` (see `worklog.md`) against
a Postgres server that already exists somewhere reachable from the data plane's
network -- e.g. a container on the k3d docker network, a VM, or a managed database --
rather than one OpenChoreo provisions itself (contrast with
`samples/from-image/doclet/resources/postgres.yaml`, which provisions an in-cluster
StatefulSet).

It's owned by the `doclet` project (`samples/from-image/doclet/project.yaml`) so it can
be consumed as a resource dependency of `doclet-document` -- resource dependencies must
live in the **same project** as the consuming Component (no cross-project support yet,
see `WorkloadResourceDependency`'s doc comment), so this can't sit in an unrelated
project like `default` and still be tunnellable from a doclet workload.

⚠️ **All outputs, including `password`, are plain non-secret values** (see the
ClusterResourceType's description). That's deliberate: `occ dev connect` v1 doesn't
resolve secret-kind outputs yet (worklog D3/D12), so this lets you exercise a fully
authenticated connection through the tunnel today. Don't reuse this ResourceType for
anything but a throwaway local test database.

## Apply

```bash
kubectl apply -f samples/dev-connect-external-postgres/cluster-resource-type.yaml
kubectl apply -f samples/dev-connect-external-postgres/resource.yaml
kubectl apply -f samples/dev-connect-external-postgres/binding-development.yaml
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

`samples/from-image/doclet/components/service-document.yaml` already declares it:

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

Any other Component in the **`doclet` project** could reference it the same way; a
Component in a different project cannot (see the note above).

Then:

```bash
occ dev connect --workload samples/from-image/doclet/components/service-document.yaml \
  --namespace default --env development
```

tunnels to `172.18.0.4:5432` and, unlike the doclet's in-cluster Postgres, hands you a
working `$DB_PASSWORD` too.
