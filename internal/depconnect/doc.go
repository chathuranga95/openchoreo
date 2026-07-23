// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package depconnect defines the wire contract shared by the `occ local`
// client and the control plane: the resolve request/response (resolve.go) and the
// CP-signed capability that authorizes a tunnel session (capability.go).
//
// Transport model (see worklog.md §8): occ resolves a workload's dependencies
// against the control plane, which returns a set of targets plus a signed
// capability. To tunnel a target, occ opens a WebSocket to the control plane's
// dep-connect stream endpoint, presenting the capability; the control plane
// verifies it locally (no extra round trip) and relays the stream through the
// existing cluster-gateway/cluster-agent management tunnel to the resolved
// host:port. There is no separate dev-tunnel agent or new data-plane ingress.
package depconnect
