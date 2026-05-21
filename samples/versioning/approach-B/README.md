<!--
Copyright 2026 The OpenChoreo Authors
SPDX-License-Identifier: Apache-2.0
-->

# Approach B: Generic Versioned HTTP Route Trait

This approach keeps versioning out of component-specific ComponentTypes. It defines one reusable `versioned-http-route` Trait and one generic `versioned-service` ComponentType that allows that Trait.

The Trait patches the external HTTPRoute emitted by the ComponentType:

- `hostname` groups multiple component versions under one public host.
- `version` becomes the URL path prefix, for example `/v1` or `/v2`.

That means these two components:

- `greeter-b-v1` with `hostname: greeter.openchoreoapis.localhost` and `version: v1`
- `greeter-b-v2` with `hostname: greeter.openchoreoapis.localhost` and `version: v2`

resolve as:

- `http://greeter.openchoreoapis.localhost:19080/v1/greeter/greet?name=Codex`
- `http://greeter.openchoreoapis.localhost:19080/v2/greeter/greet?name=Codex`

The same Trait can be reused for other service families:

- `foov1`: `hostname: foo.openchoreoapis.localhost`, `version: v1`
- `barv1`: `hostname: bar.openchoreoapis.localhost`, `version: v1`

## How to run

```bash
kubectl apply -f samples/versioning/approach-B/01-greeter.yaml

kubectl wait --for=condition=WorkflowSucceeded \
  workflowrun \
  -l openchoreo.dev/component=greeter-b-v1 \
  -n default --timeout=20m

kubectl wait --for=condition=WorkflowSucceeded \
  workflowrun \
  -l openchoreo.dev/component=greeter-b-v2 \
  -n default --timeout=20m

kubectl apply -f samples/versioning/approach-B/02-workloads.yaml
kubectl apply -f samples/versioning/approach-B/03-enable-auto-deploy.yaml
```
