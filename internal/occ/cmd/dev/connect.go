// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package dev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/occ/auth"
	"github.com/openchoreo/openchoreo/internal/occ/cmd/config"
)

// resolveRequest mirrors the openchoreo-api resolve-dependencies request body.
type resolveRequest struct {
	Project     string                                  `json:"project"`
	Environment string                                  `json:"environment"`
	Connections []openchoreov1alpha1.WorkloadConnection `json:"connections"`
}

// resolvedDependency mirrors one entry of the resolve-dependencies response.
type resolvedDependency struct {
	Project     string                                   `json:"project"`
	Component   string                                   `json:"component"`
	Endpoint    string                                   `json:"endpoint"`
	Visibility  string                                   `json:"visibility"`
	Type        string                                   `json:"type"`
	Scheme      string                                   `json:"scheme"`
	Host        string                                   `json:"host"`
	Port        int32                                    `json:"port"`
	Path        string                                   `json:"path"`
	Address     string                                   `json:"address"`
	EnvBindings openchoreov1alpha1.ConnectionEnvBindings `json:"envBindings"`
}

type pendingDependency struct {
	Component string `json:"component"`
	Endpoint  string `json:"endpoint"`
	Reason    string `json:"reason"`
}

type resolveResponse struct {
	Resolved []resolvedDependency `json:"resolved"`
	Pending  []pendingDependency  `json:"pending"`
}

func newConnectCmd() *cobra.Command {
	var workloadPath, environment, namespace string
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Open a subshell wired to a workload's dependencies in an environment",
		Long: `connect resolves the dependencies declared in a workload file, opens a local
TCP tunnel to each one in the given environment, and starts a subshell where
each dependency is reachable on localhost (with the workload's env-var bindings
set). Exiting the subshell tears down the tunnels.`,
		PreRunE: auth.RequireLogin(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConnect(cmd.Context(), workloadPath, environment, namespace)
		},
	}
	cmd.Flags().StringVar(&workloadPath, "workload", "", "path to the workload YAML file (required)")
	cmd.Flags().StringVar(&environment, "environment", "", "target environment (required)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "namespace (defaults to the current context)")
	_ = cmd.MarkFlagRequired("workload")
	_ = cmd.MarkFlagRequired("environment")
	return cmd
}

func runConnect(ctx context.Context, workloadPath, environment, namespace string) error {
	// Parse the workload and pull out its declared endpoint dependencies.
	content, err := os.ReadFile(workloadPath)
	if err != nil {
		return fmt.Errorf("failed to read workload file: %w", err)
	}
	var wl openchoreov1alpha1.Workload
	if err := yaml.Unmarshal(content, &wl); err != nil {
		return fmt.Errorf("failed to parse workload YAML: %w", err)
	}
	connections := wl.Spec.GetDependencyEndpoints()
	if len(connections) == 0 {
		return fmt.Errorf("workload %q declares no dependency endpoints", workloadPath)
	}

	// Resolve namespace + consumer project from context / workload owner.
	cctx, err := config.GetCurrentContext()
	if err != nil {
		return err
	}
	if namespace == "" {
		namespace = cctx.Namespace
	}
	if namespace == "" {
		return fmt.Errorf("namespace is required (set --namespace or a context namespace)")
	}
	project := wl.Spec.Owner.ProjectName
	if project == "" {
		project = cctx.Project
	}

	controlPlane, err := config.GetCurrentControlPlane()
	if err != nil {
		return err
	}
	token, err := getToken()
	if err != nil {
		return err
	}

	// Resolve the dependencies to in-cluster addresses.
	resp, err := resolveDependencies(ctx, controlPlane.URL, token, namespace, resolveRequest{
		Project:     project,
		Environment: environment,
		Connections: connections,
	})
	if err != nil {
		return err
	}
	for _, p := range resp.Pending {
		fmt.Fprintf(os.Stderr, "warning: dependency %s/%s not resolved: %s\n", p.Component, p.Endpoint, p.Reason)
	}
	if len(resp.Resolved) == 0 {
		return fmt.Errorf("no dependencies could be resolved in environment %q", environment)
	}

	// Open a local listener per resolved dependency and assemble the subshell env.
	sess := &connectSession{
		baseURL:   controlPlane.URL,
		token:     token,
		namespace: namespace,
		project:   project,
		env:       environment,
	}
	var envVars []string
	var summary []string
	for _, dep := range resp.Resolved {
		ln, localPort, lerr := listenLocal(dep.Port)
		if lerr != nil {
			sess.closeAll()
			return fmt.Errorf("failed to open local listener for %s/%s: %w", dep.Component, dep.Endpoint, lerr)
		}
		sess.listeners = append(sess.listeners, ln)
		envVars = append(envVars, buildEnvVars(dep, localPort)...)
		summary = append(summary, fmt.Sprintf("  %-30s localhost:%-5d -> %s:%d (%s)",
			dep.Component+"/"+dep.Endpoint, localPort, dep.Host, dep.Port, dep.Scheme))
		go sess.serve(ln, dep)
	}

	fmt.Fprintf(os.Stderr, "\nConnected to environment %q. Dependencies tunneled locally:\n%s\n\nStarting subshell — type 'exit' to close the tunnels.\n\n",
		environment, strings.Join(summary, "\n"))

	runErr := runSubshell(envVars)
	sess.closeAll()
	fmt.Fprintln(os.Stderr, "\nTunnels closed.")
	return runErr
}

// connectSession holds the live listeners for a `dev connect` invocation.
type connectSession struct {
	baseURL   string
	token     string
	namespace string
	project   string
	env       string

	listeners []net.Listener
	mu        sync.Mutex
	closed    bool
}

func (s *connectSession) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for _, ln := range s.listeners {
		_ = ln.Close()
	}
}

// serve accepts local connections and bridges each one over a tunnel WebSocket.
func (s *connectSession) serve(ln net.Listener, dep resolvedDependency) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed during teardown
		}
		go s.bridge(conn, dep)
	}
}

// bridge wires a local TCP connection to the api /tunnel WebSocket, copying raw
// bytes in both directions.
func (s *connectSession) bridge(local net.Conn, dep resolvedDependency) {
	defer local.Close()

	wsURL, err := buildTunnelWSURL(s.baseURL, s.namespace, s.project, s.env, dep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tunnel error (%s/%s): %v\n", dep.Component, dep.Endpoint, err)
		return
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+s.token)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		fmt.Fprintf(os.Stderr, "tunnel dial failed (%s/%s, http %d): %v\n", dep.Component, dep.Endpoint, code, err)
		return
	}
	defer conn.Close()

	done := make(chan struct{}, 2)
	var wmu sync.Mutex // gorilla panics on concurrent writes

	// local -> ws
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := local.Read(buf)
			if n > 0 {
				wmu.Lock()
				werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n])
				wmu.Unlock()
				if werr != nil {
					return
				}
			}
			if rerr != nil {
				wmu.Lock()
				_ = conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				wmu.Unlock()
				return
			}
		}
	}()

	// ws -> local
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, msg, rerr := conn.ReadMessage()
			if rerr != nil {
				return
			}
			if _, werr := local.Write(msg); werr != nil {
				return
			}
		}
	}()

	<-done
}

// getToken returns a valid access token, refreshing it if expired.
func getToken() (string, error) {
	cred, err := config.GetCurrentCredential()
	if err != nil {
		return "", err
	}
	tok := cred.Token
	if tok == "" || auth.IsTokenExpired(tok) {
		tok, err = auth.RefreshToken()
		if err != nil {
			return "", fmt.Errorf("failed to obtain access token: %w", err)
		}
	}
	return tok, nil
}

func resolveDependencies(ctx context.Context, baseURL, token, namespace string, body resolveRequest) (*resolveResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v1/namespaces/" + namespace + "/dependencies/resolve"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	httpResp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resolve request failed (status %d): %s",
			httpResp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out resolveResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("failed to parse resolve response: %w", err)
	}
	return &out, nil
}

// listenLocal binds 127.0.0.1:preferredPort, falling back to an ephemeral port
// if that is unavailable. Returns the listener and the actual local port.
func listenLocal(preferredPort int32) (net.Listener, int, error) {
	if preferredPort > 0 {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort)); err == nil {
			return ln, int(preferredPort), nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	return ln, ln.Addr().(*net.TCPAddr).Port, nil
}

// buildEnvVars produces NAME=VALUE entries for a dependency's env bindings,
// pointed at the local tunnel mouth instead of the in-cluster address.
func buildEnvVars(dep resolvedDependency, localPort int) []string {
	var vars []string
	const host = "127.0.0.1"
	if dep.EnvBindings.Address != "" {
		vars = append(vars, dep.EnvBindings.Address+"="+localAddress(dep.Scheme, host, localPort, dep.Path))
	}
	if dep.EnvBindings.Host != "" {
		vars = append(vars, dep.EnvBindings.Host+"="+host)
	}
	if dep.EnvBindings.Port != "" {
		vars = append(vars, dep.EnvBindings.Port+"="+strconv.Itoa(localPort))
	}
	if dep.EnvBindings.BasePath != "" {
		vars = append(vars, dep.EnvBindings.BasePath+"="+dep.Path)
	}
	return vars
}

// localAddress formats a connection string for the local tunnel mouth, matching
// the server's address formatting (scheme://host:port/path vs host:port).
func localAddress(scheme, host string, port int, path string) string {
	var sb strings.Builder
	switch scheme {
	case "http", "https", "ws", "wss", "tls":
		sb.WriteString(scheme)
		sb.WriteString("://")
	}
	sb.WriteString(host)
	sb.WriteString(":")
	sb.WriteString(strconv.Itoa(port))
	if path != "" {
		if !strings.HasPrefix(path, "/") {
			sb.WriteString("/")
		}
		sb.WriteString(path)
	}
	return sb.String()
}

func buildTunnelWSURL(baseURL, namespace, project, env string, dep resolvedDependency) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = fmt.Sprintf("/tunnel/namespaces/%s/components/%s", namespace, dep.Component)
	q := u.Query()
	q.Set("env", env)
	q.Set("project", project)
	q.Set("endpoint", dep.Endpoint)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// runSubshell launches the user's shell with the tunnel env vars added. A
// non-zero shell exit is not treated as an occ error.
func runSubshell(extraEnv []string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	c := exec.Command(shell)
	c.Env = append(os.Environ(), extraEnv...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// Let the interactive subshell own Ctrl-C rather than killing occ.
	signal.Ignore(syscall.SIGINT)
	defer signal.Reset(syscall.SIGINT)

	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return err
	}
	return nil
}
