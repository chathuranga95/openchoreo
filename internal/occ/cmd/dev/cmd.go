// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package dev

import "github.com/spf13/cobra"

// NewDevCmd builds the `dev` command group for local development workflows.
func NewDevCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Local development workflows",
		Long:  "Commands that bridge OpenChoreo cluster resources into your local environment.",
	}
	cmd.AddCommand(newConnectCmd())
	return cmd
}
