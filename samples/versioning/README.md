<!--
Copyright 2026 The OpenChoreo Authors
SPDX-License-Identifier: Apache-2.0
-->

# Versioning Samples

These samples show how to expose two OpenChoreo Components as versioned HTTP endpoints on the same host, using `/v1` and `/v2` path prefixes.
These samples are created to demonstrate the openchoreo capability to rollout multiple versions of a same source and maintain them simultaneously.

- `approach-A`: puts versioned HTTPRoute rendering directly in a ComponentType.
- `approach-B (Recommended)`: uses a reusable Trait to apply versioned routing to a generic service ComponentType.
