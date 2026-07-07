# Recommended Direction for Local Development

OpenChoreo needs a better local development story, but application developers and platform engineers want fundamentally different things from "local". Rather than solving both with one mechanism, this proposal separates the two personas and gives each a fast default plus an advanced escalation path.

## Application developer experience

For application developers, the main pain is the slow inner loop: change code, push, build, deploy, debug, and repeat. Their primary need is to run the application locally, debug it with standard tools, and connect to the same upstream dependencies used by the workload.

The key observation is that the only meaningful gap between the shared dev cluster and the developer's laptop is the dependencies. The application code, the debugger, and the language runtime are already local; what is missing is reachability to the databases, queues, and services the workload talks to. The two options below are two ways to close that dependency gap — a fast default, and a heavier option for advanced debugging.

### Option 1 — `occ` dependency forwarding to the local computer (default, fast)

Since `workload.yaml` is already committed to the application source repository and defines endpoints, configurations, and dependencies, `occ` should use it as the source of truth for local dependency forwarding.

For example, when `workload.yaml` defines an endpoint dependency:

```
dependencies:
  endpoints:
    - component: postgres-db
      name: api
      visibility: project
      envBindings:
        host: DB_HOST
        port: DB_PORT
```

`occ` can read this dependency definition, locate the matching dependency in the development environment, establish the required port-forwarding locally, and generate local environment bindings such as:

```
DB_HOST=localhost
DB_PORT=<local-forwarded-port>
```

The same approach should apply to resource dependencies as well. If the workload declares resource dependencies, `occ dev connect` should resolve the active resource binding for the selected environment, read the declared outputs, and make them available to the local application.

For example:

```
dependencies:
  resources:
    - ref: doclet-postgres
      envBindings:
        host: DB_HOST
        port: DB_PORT
        username: DB_USER
        password: DB_PASSWORD
        database: DB_NAME
      fileBindings:
        ca-bundle: /tmp/openchoreo/certs/ca-bundle.pem
```

For this, `occ` should do the following based on the user access level:

* resolve the resource dependency for the selected environment  
* expose network-backed resources locally where needed  
* generate local environment variables from `envBindings`  
* materialize file-based outputs from `fileBindings` into a safe local directory  
* handle sensitive values carefully, without unnecessarily printing secrets to stdout

A possible workflow could be:

```
occ dev connect --workload workload.yaml --env development
occ dev disconnect
```

`occ dev connect` would establish the required local connections, prepare resource outputs and generate the environment variables needed by the local application.

This gives application developers a lightweight local debugging experience without requiring them to run a local OpenChoreo cluster. The application runs locally, while `occ` connects only to the dependencies declared in the committed workload descriptor. The full feasibility and design for this approach is in [The tunnel option in depth (local computer ↔ dev cluster)](#the-tunnel-option-in-depth-local-computer--dev-cluster) below.

### Option 2 — local k3d joined to the dev cluster by a tunnel (advanced debugging)

When plain dependency forwarding is not enough — for example, when the workload must run inside a real Kubernetes runtime to reproduce in-cluster behavior such as sidecars, init containers, service discovery, or network policy — the workload can instead run in a local k3d cluster with the dev cluster's dependencies tunnelled in. This is slower to set up and heavier to run than Option 1, but gives a much closer reproduction of the real environment.

Unlike Option 1, the workload here runs as a real pod *inside* k3d rather than as a native laptop process, so the first step is getting the developer's working source into the cluster **without a `git push`**. The developer's source directory is mounted from the host machine into the local k3d cluster and built in place, so an edit becomes a running workload through a purely local build — no commit, no push, no CI round-trip in the inner loop. A custom workflow supports this. The full design — source mounting first, then the tunnel — is in [The tunnel option in depth (local k3d ↔ dev cluster)](#the-tunnel-option-in-depth-local-k3d--dev-cluster) below.

A possible workflow could be:

```
# 1. build the host-mounted source into the local k3d cluster (no git push)
occ component build <component>

# 2. tunnel the dev cluster's dependencies into k3d, then iterate
occ dev tunnel up --workload workload.yaml --env development
occ dev tunnel status
occ dev tunnel down
```

`occ component build` builds the host-mounted source into the local cluster and deploys it as a workload, and `occ dev tunnel up` then forwards the workload's declared dependencies in so the pod reaches them by their normal in-cluster names — the tunnel stays open while you edit and rebuild, until `occ dev tunnel down`.

## Platform engineer experience

For platform engineers, the need is different. It is not dependency resolution — it is a local development playground that approximates the real platform closely enough to test risky platform-level changes such as installation, configuration, ComponentTypes, traits, controllers, workflows, policies, RBAC, and governance behavior, but that runs entirely on their own machine. The two options below differ by how close an approximation the change under test requires.

### Option 1 — fully isolated lightweight local k3d cluster (infra change testing)

For most platform experimentation, a lightweight local k3d-based OpenChoreo setup is sufficient. It is fully local, disposable, and fast to recreate.

However, this local k3d setup should be documented as an isolated platform experimentation environment, not as a replica of the organization's shared dev cluster. Its limitations should be clearly stated, because local k3d will not fully match the real dev environment in areas such as:

* Identity and governance  
* Cloud integrations and networking  
* Observability and scale  
* Storage and organization-specific policies

### Option 2 — local k3d joined to the dev cluster by a tunnel (advanced debugging)

When the isolated playground diverges too far from the real environment — and a platform change can only be validated against real dependencies or other teams' services running in the dev cluster — the same local k3d cluster can be tunnelled to the dev cluster. This is the heavier, more capable option shared with application developers, described in [The tunnel option in depth (local k3d ↔ dev cluster)](#the-tunnel-option-in-depth-local-k3d--dev-cluster) below.

## The tunnel option in depth (local computer ↔ dev cluster)
see: tunneling-dev-cluster-into-local-computer-feasibility.md

This is the app-developer default (Option 1): the app runs natively on the laptop and `occ dev connect` forwards the dev cluster's dependencies down to `localhost`, materializing the env vars and files the workload declares. It is materially lighter than the local-k3d tunnel — no local cluster, no stub Services, no in-cluster DNS, and no fork of the dependency-resolution contract — while sharing the one core capability the k3d path also needs (raw-TCP carriage through the cluster-gateway/agent channel) and the one policy decision (secret egress to the laptop, scoped and audited). The same materialized dependencies also become available to the developer's local AI coding agents, which the feasibility doc covers in depth.

## The tunnel option in depth (local k3d ↔ dev cluster)
see: tunneling-dev-cluster-into-local-k3d-feasibility.md

This is the advanced escalation shared by both personas: the workload runs as a real pod in a local k3d cluster, and the dev cluster's dependencies are tunnelled in so the pod reaches them by their normal in-cluster names. It has two distinct steps, in order:

1. **Mount the source into k3d and build it there — no `git push` (local source build).** The developer keeps their working source in a host directory (e.g. `~/openchoreo-local-source/<component>`) that is bind-mounted into the local k3d cluster (k3d's `--volume`) at `/var/openchoreo/local-source`. A `prepare-local-source` build step copies that host-mounted source into the build workflow's volume and computes a content-hash image tag (so a rebuild with no source change is a no-op); the normal `containerfile-build → publish-image-k3d → generate-workload-k3d` steps then build the image into the in-cluster registry and produce the workload. The developer triggers this with `occ component build <component>` (or from the Backstage UI) — no commit, no push, no CI. Mechanically, this just swaps the git-based `checkout-source` step the cloud build uses for a host-mount source step, which is the single change that removes the version-control round-trip from the inner loop. This step is what makes the advanced path usable for fast iteration; without it, every edit would still need a `git push`.
2. **Tunnel the dev cluster's dependencies into k3d.** Once the workload is running in k3d, `occ dev tunnel up` resolves the workload's declared dependencies against the dev environment and makes them reachable inside the local cluster under the same in-cluster names the workload would use in the cloud. The full feasibility and design for this step is in the companion doc.

## YAML validation and dry-run support

In addition to dependency forwarding, `occ` should provide a `--dry-run` capability similar to `kubectl`. This would allow both application developers and platform engineers to validate OpenChoreo YAML files before applying or relying on them.

This should support OpenChoreo CRDs as well as config descriptors such as `workload.yaml`.

For example:

```
occ apply -f component-type.yaml --dry-run
occ validate -f workload.yaml
```

The goal is to catch schema errors, invalid fields, missing required values, and unsupported OpenChoreo constructs early, without making any changes to the target environment. This gives developers a lightweight way to validate platform contracts locally while still keeping the shared dev environment as the real integration checkpoint.

## Summary

My recommendation is to split the local development story by persona, giving each a fast default and an advanced escalation path:

1. **Application developers** close the dependency gap with `occ` dependency forwarding to the local computer (default, fast), and escalate to the local k3d ↔ dev cluster tunnel when advanced, in-cluster debugging is required.
2. **Platform engineers** get a fully isolated lightweight local k3d cluster as a development playground for infra change testing (default), and escalate to the same local k3d ↔ dev cluster tunnel when a change can only be validated against the real dev environment.
3. **Cross-cutting:** `occ` dry-run and validation tooling serves both personas across all options.

This provides application developers with a fast local debugging workflow and platform engineers with a safe, disposable environment for risky platform experimentation, with a shared advanced path for the cases that need a closer reproduction. The shared dev environment remains the real integration checkpoint.
