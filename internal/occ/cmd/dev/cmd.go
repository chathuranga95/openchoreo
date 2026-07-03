// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package dev implements the `occ dev` command group for local development against
// OpenChoreo, starting with `occ dev connect` (tunnel a workload's dependencies to
// the developer's machine — see worklog.md).
package dev

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/openchoreo/openchoreo/internal/occ/auth"
	"github.com/openchoreo/openchoreo/internal/occ/flags"
)

// NewDevCmd builds the `dev` command group.
func NewDevCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Local development commands",
		Long:  "Commands that support local development against OpenChoreo.",
	}
	cmd.AddCommand(newConnectCmd())
	return cmd
}

func newConnectCmd() *cobra.Command {
	var (
		workloadPath string
		printEnv     bool
	)

	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Tunnel a workload's dependencies to your machine",
		Long: "Resolve a workload's declared dependencies for an environment, open a local " +
			"TCP listener for each, and start a subshell whose environment points at those " +
			"listeners — so the app runs locally against the environment's real upstreams.",
		Example: "  occ dev connect --workload workload.yaml --env development",
		PreRunE: auth.RequireLogin(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolver, err := newHTTPResolver()
			if err != nil {
				return err
			}
			// Tunnels live for the session; Ctrl-C / SIGTERM tears everything down.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return New(resolver).Connect(ctx, ConnectParams{
				WorkloadPath: workloadPath,
				Namespace:    flags.GetNamespace(cmd),
				Environment:  flags.GetEnvironment(cmd),
				PrintEnv:     printEnv,
			}, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVar(&workloadPath, "workload", "", "Path to the workload.yaml file (required)")
	cmd.Flags().BoolVar(&printEnv, "print-env", false,
		"Print resolved env bindings and hold tunnels open instead of spawning a subshell")
	_ = cmd.MarkFlagRequired("workload")
	flags.AddNamespace(cmd)
	flags.AddEnvironment(cmd)
	return cmd
}
