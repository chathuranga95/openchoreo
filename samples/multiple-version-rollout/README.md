<!--
Copyright 2026 The OpenChoreo Authors
SPDX-License-Identifier: Apache-2.0
-->

# Multiple version rollout from a single source

This sample shows how to run two versions of the same service at the same time.
Both versions come from one source repository and are exposed as versioned HTTP endpoints.

The example uses two OpenChoreo Components:

- one component for `v1`
- one component for `v2`

Each version has its own workload and build revision. Traffic is split by path prefix, such as `/v1` and `/v2`.

## Expected result

After deployment, the versions are available through separate paths on a shared host:

- `http://<host>/v1/greeter/greet?name=John`
- `http://<host>/v2/greeter/greet?name=John`

## Approaches

| Approach | Pattern | Use when |
| --- | --- | --- |
| [`approach-A`](./approach-A/) | The `ComponentType` renders version-aware HTTPRoutes directly. | The routing behavior is specific to one service family. |
| [`approach-B`](./approach-B/) | A reusable `Trait` patches the HTTPRoute rendered by a generic `ComponentType`. | The same versioned routing model should work across services. |

`approach-B` is the recommended option. It keeps the routing concern reusable and avoids creating a new ComponentType for each service family.

## How to use this sample

Choose an approach and follow the README in that directory:

- [`approach-A/README.md`](./approach-A/README.md)
- [`approach-B/README.md`](./approach-B/README.md)
