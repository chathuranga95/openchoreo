// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package dev

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"

	k8syaml "sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/devconnect"
)

// Dev implements the `dev` command group logic.
type Dev struct {
	resolver Resolver
	// dialTunnel opens the tunnel to the dev-agent; overridable in tests.
	dialTunnel func(agent devconnect.AgentEndpoint, capability string) (*devconnect.TunnelClient, error)
	// runShell spawns the subshell with the given environment; overridable in tests.
	runShell func(ctx context.Context, env []string) error
}

// New builds a Dev with production defaults.
func New(resolver Resolver) *Dev {
	return &Dev{
		resolver: resolver,
		dialTunnel: func(agent devconnect.AgentEndpoint, capability string) (*devconnect.TunnelClient, error) {
			return devconnect.DialTLS(agent.Endpoint, agent.CABundle, capability)
		},
		runShell: runInteractiveShell,
	}
}

// Connect resolves the workload's dependencies, opens a local listener per tunnellable
// target, renders env bindings pointing at those listeners, and spawns a subshell
// (or prints the env with --print-env). Tunnels live until the subshell exits or ctx
// is cancelled.
func (d *Dev) Connect(ctx context.Context, p ConnectParams, out io.Writer) error {
	if p.WorkloadPath == "" {
		return fmt.Errorf("--workload is required")
	}
	if p.Environment == "" {
		return fmt.Errorf("--env is required")
	}

	wl, err := loadWorkloadFromFile(p.WorkloadPath)
	if err != nil {
		return err
	}

	namespace := wl.Namespace
	if namespace == "" {
		namespace = p.Namespace
	}
	if namespace == "" {
		return fmt.Errorf("namespace is required: set metadata.namespace in the workload file or pass --namespace")
	}

	resp, err := d.resolver.Resolve(ctx, buildResolveRequest(wl, namespace, p.Environment))
	if err != nil {
		return err
	}

	tc, err := d.dialTunnel(resp.Agent, resp.Capability)
	if err != nil {
		return err
	}
	defer tc.Close()

	overrides := map[string]string{}
	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	fmt.Fprintf(out, "Connected to %s/%s (%s):\n", wl.Spec.Owner.ProjectName, wl.Spec.Owner.ComponentName, p.Environment)
	for _, t := range resp.Targets {
		ln, lerr := net.Listen("tcp", "127.0.0.1:0")
		if lerr != nil {
			return fmt.Errorf("open local listener for %s: %w", t.Key, lerr)
		}
		listeners = append(listeners, ln)
		port := ln.Addr().(*net.TCPAddr).Port
		go forward(ln, tc, t.Key)

		maps.Copy(overrides, devconnect.RenderEnv(t, "127.0.0.1", port))
		fmt.Fprintf(out, "  %-28s -> 127.0.0.1:%d  (%s)\n", t.Key, port, targetKind(t))
	}
	for _, u := range resp.Unconnectable {
		fmt.Fprintf(out, "  ! %s: %s\n", u.Ref, u.Reason)
	}

	if p.PrintEnv {
		printEnvBindings(out, overrides)
		fmt.Fprintln(out, "\nTunnels open. Press Ctrl-C to disconnect.")
		<-ctx.Done()
		return nil
	}

	fmt.Fprintln(out, "\nStarting subshell with dependency env. Type 'exit' to disconnect.")
	return d.runShell(ctx, mergeEnv(os.Environ(), overrides))
}

// forward accepts local connections and pipes each over a fresh tunnel stream.
func forward(ln net.Listener, tc *devconnect.TunnelClient, key string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go func(local net.Conn) {
			stream, serr := tc.OpenStream(key)
			if serr != nil {
				_ = local.Close()
				return
			}
			devconnect.Pipe(local, stream)
		}(conn)
	}
}

func targetKind(t devconnect.ResolvedTarget) string {
	if t.Resource != nil {
		return "resource"
	}
	return "endpoint"
}

// loadWorkloadFromFile reads a YAML file and returns its Workload document.
func loadWorkloadFromFile(path string) (*v1alpha1.Workload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workload file: %w", err)
	}
	for _, doc := range splitYAMLDocs(data) {
		var probe struct {
			Kind string `json:"kind"`
		}
		if err := k8syaml.Unmarshal(doc, &probe); err != nil {
			continue
		}
		if probe.Kind != "Workload" {
			continue
		}
		var wl v1alpha1.Workload
		if err := k8syaml.Unmarshal(doc, &wl); err != nil {
			return nil, fmt.Errorf("parse Workload: %w", err)
		}
		return &wl, nil
	}
	return nil, fmt.Errorf("no Workload document found in %s", path)
}

// splitYAMLDocs splits a multi-document YAML byte slice on `---` separators.
func splitYAMLDocs(data []byte) [][]byte {
	var docs [][]byte
	for part := range bytes.SplitSeq(data, []byte("\n---")) {
		if trimmed := bytes.TrimSpace(part); len(trimmed) > 0 {
			docs = append(docs, trimmed)
		}
	}
	return docs
}

// buildResolveRequest maps a Workload's declared dependencies into a ResolveRequest.
func buildResolveRequest(wl *v1alpha1.Workload, namespace, env string) devconnect.ResolveRequest {
	req := devconnect.ResolveRequest{
		Namespace:   namespace,
		Project:     wl.Spec.Owner.ProjectName,
		Component:   wl.Spec.Owner.ComponentName,
		Environment: env,
	}
	deps := wl.Spec.Dependencies
	if deps == nil {
		return req
	}
	for _, e := range deps.Endpoints {
		req.Endpoints = append(req.Endpoints, devconnect.EndpointDep{
			Project:    e.Project,
			Component:  e.Component,
			Name:       e.Name,
			Visibility: e.Visibility,
			EnvBindings: devconnect.EndpointEnvBindings{
				Address:  e.EnvBindings.Address,
				Host:     e.EnvBindings.Host,
				Port:     e.EnvBindings.Port,
				BasePath: e.EnvBindings.BasePath,
			},
		})
	}
	for _, r := range deps.Resources {
		req.Resources = append(req.Resources, devconnect.ResourceDep{
			Ref:         r.Ref,
			EnvBindings: r.EnvBindings,
		})
	}
	return req
}

// mergeEnv overlays overrides onto a base environment ("KEY=VALUE" slice).
func mergeEnv(base []string, overrides map[string]string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			merged[k] = v
		}
	}
	maps.Copy(merged, overrides)
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func printEnvBindings(out io.Writer, env map[string]string) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(out, "\nEnvironment bindings:")
	for _, k := range keys {
		fmt.Fprintf(out, "  export %s=%s\n", k, env[k])
	}
}

func runInteractiveShell(ctx context.Context, env []string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(ctx, shell)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
