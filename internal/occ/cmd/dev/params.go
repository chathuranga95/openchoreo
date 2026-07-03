// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package dev

// ConnectParams are the inputs to `occ dev connect`.
type ConnectParams struct {
	// WorkloadPath is the path to the local workload.yaml (source of the consuming
	// component identity and declared dependencies — worklog D11).
	WorkloadPath string
	// Namespace is the control-plane namespace (org). Falls back to the workload
	// file's metadata.namespace when set there.
	Namespace string
	// Environment is the target environment to resolve dependencies for.
	Environment string
	// PrintEnv, when set, prints the resolved env bindings and holds the tunnels open
	// instead of spawning a subshell.
	PrintEnv bool
}
