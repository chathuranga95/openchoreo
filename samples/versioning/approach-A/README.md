<!--
Copyright 2026 The OpenChoreo Authors
SPDX-License-Identifier: Apache-2.0
-->

# Approach A: ComponentType-Owned Versioned Routes

This approach creates a namespace-scoped `deployment/greeter-versioned-service` ComponentType whose external HTTPRoute template directly supports URL versioning.

The ComponentType accepts these parameters on each Component:

- `hostname`: shared public host for the service family.
- `version`: route name segment, for example `v1` or `v2`.
- `pathPrefix`: public URL path prefix, for example `/v1` or `/v2`.

The ComponentType emits the Deployment, Service, and HTTPRoute resources. Its HTTPRoute template uses the shared hostname and the per-component path prefix, while preserving the Workload endpoint `basePath` through a `URLRewrite`.

That means these two Components:

- `docker-go-greeter-v1` from the `main` branch, with `pathPrefix: /v1`
- `docker-go-greeter-v2` from the `experimental` branch, with `pathPrefix: /v2`

resolve as:

- `http://docker-go-greeter.openchoreoapis.localhost:19080/v1/greeter/greet?name=Codex`
- `http://docker-go-greeter.openchoreoapis.localhost:19080/v2/greeter/greet?name=Codex`

## Trade-Off

This is the simplest rendering model because the version-aware HTTPRoute is part of the ComponentType itself. The downside is that versioning is coupled to this ComponentType, so reusing the same behavior for another service family usually means creating another similar ComponentType or generalizing this one further.

For a more reusable versioning concern, see `../approach-B`, where a generic ComponentType allows a reusable Trait to patch the generated HTTPRoute.

## How to run

```bash
kubectl apply -f samples/versioning/approach-A/01-docker-go-greeter.yaml

kubectl wait --for=condition=WorkflowSucceeded \
  workflowrun \
  -l openchoreo.dev/component=docker-go-greeter-v1 \
  -n default --timeout=20m

kubectl wait --for=condition=WorkflowSucceeded \
  workflowrun \
  -l openchoreo.dev/component=docker-go-greeter-v2 \
  -n default --timeout=20m

kubectl apply -f samples/versioning/approach-A/02-workloads.yaml
kubectl apply -f samples/versioning/approach-A/03-enable-auto-deploy.yaml
```
