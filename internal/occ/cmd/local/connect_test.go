// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchoreo/openchoreo/internal/depconnect"
)

type fakeResolver struct{ resp *depconnect.ResolveResponse }

func (f *fakeResolver) Resolve(context.Context, depconnect.ResolveRequest) (*depconnect.ResolveResponse, error) {
	return f.resp, nil
}

// startEchoServer starts a plain TCP echo server, standing in for the far end of a
// dep-connect stream (in production, this is the tunnelled dependency).
func startEchoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	return ln
}

const testWorkloadYAML = `apiVersion: openchoreo.dev/v1alpha1
kind: Component
metadata:
  name: doclet-document
---
apiVersion: openchoreo.dev/v1alpha1
kind: Workload
metadata:
  name: doclet-document
spec:
  owner:
    projectName: doclet
    componentName: doclet-document
  dependencies:
    resources:
      - ref: doclet-postgres
        envBindings:
          host: DB_HOST
          port: DB_PORT
          database: DB_NAME
`

func writeWorkloadFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workload.yaml")
	if err := os.WriteFile(path, []byte(testWorkloadYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envToMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// TestConnectEndToEnd exercises Dev.Connect with a fake dialStream that connects
// straight to a local echo server, standing in for the real occ -> openchoreo-api ->
// cluster-gateway -> cluster-agent -> dependency chain (worklog §8). That chain's
// own hops are covered independently in their own packages (depconnect, cluster-agent,
// cluster-gateway, openchoreo-api/handlers); this test's job is Dev.Connect's local
// plumbing: listeners, env rendering, and per-connection dialing.
func TestConnectEndToEnd(t *testing.T) {
	echo := startEchoServer(t)

	resp := &depconnect.ResolveResponse{
		Capability: "test-capability",
		Targets: []depconnect.ResolvedTarget{{
			Key:   "res/doclet-postgres",
			Proto: "tcp",
			Resource: &depconnect.ResourceRender{
				HostEnv:   "DB_HOST",
				PortEnv:   "DB_PORT",
				StaticEnv: map[string]string{"DB_NAME": "doclet"},
			},
		}},
	}

	d := New(&fakeResolver{resp: resp})

	var gotNamespace, gotComponent, gotKey, gotCapability string
	d.dialStream = func(ctx context.Context, namespace, component, key, capability string) (net.Conn, error) {
		gotNamespace, gotComponent, gotKey, gotCapability = namespace, component, key, capability
		return net.Dial("tcp", echo.Addr().String())
	}

	var gotEnv map[string]string
	roundTripErr := make(chan error, 1)
	d.runShell = func(_ context.Context, env []string) error {
		gotEnv = envToMap(env)
		roundTripErr <- tunnelRoundTrip(net.JoinHostPort(gotEnv["DB_HOST"], gotEnv["DB_PORT"]), "hello-tunnel")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Connect(ctx, ConnectParams{WorkloadPaths: []string{writeWorkloadFile(t)}, Namespace: "default", Environment: "development"}, io.Discard); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := <-roundTripErr; err != nil {
		t.Fatalf("round trip through tunnel failed: %v", err)
	}
	if gotEnv["DB_HOST"] != "127.0.0.1" {
		t.Errorf("DB_HOST = %q, want 127.0.0.1", gotEnv["DB_HOST"])
	}
	if gotEnv["DB_NAME"] != "doclet" {
		t.Errorf("DB_NAME = %q, want doclet", gotEnv["DB_NAME"])
	}
	if _, err := strconv.Atoi(gotEnv["DB_PORT"]); err != nil {
		t.Errorf("DB_PORT not numeric: %q", gotEnv["DB_PORT"])
	}

	if gotNamespace != "default" || gotComponent != "doclet-document" || gotKey != "res/doclet-postgres" || gotCapability != "test-capability" {
		t.Errorf("dialStream called with unexpected args: ns=%q component=%q key=%q capability=%q",
			gotNamespace, gotComponent, gotKey, gotCapability)
	}
}

func tunnelRoundTrip(addr, msg string) error {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(msg)); err != nil {
		return err
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != msg {
		return io.ErrUnexpectedEOF
	}
	return nil
}

const consumerWorkloadYAML = `apiVersion: openchoreo.dev/v1alpha1
kind: Component
metadata:
  name: comp1
---
apiVersion: openchoreo.dev/v1alpha1
kind: Workload
metadata:
  name: comp1
spec:
  owner:
    projectName: demo
    componentName: comp1
  dependencies:
    endpoints:
      - component: comp2
        name: http
        visibility: project
        envBindings:
          address: COMP2_URL
`

const providerWorkloadYAML = `apiVersion: openchoreo.dev/v1alpha1
kind: Component
metadata:
  name: comp2
---
apiVersion: openchoreo.dev/v1alpha1
kind: Workload
metadata:
  name: comp2
spec:
  owner:
    projectName: demo
    componentName: comp2
  endpoints:
    http:
      type: HTTP
      port: 9091
      visibility: [project]
`

func writeWorkloadFileContent(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// erroringResolver fails the test if Resolve is ever called - used to prove a
// cross-linked dependency never reaches the control plane.
type erroringResolver struct{ t *testing.T }

func (r erroringResolver) Resolve(context.Context, depconnect.ResolveRequest) (*depconnect.ResolveResponse, error) {
	r.t.Helper()
	r.t.Fatal("Resolve should not be called when every dependency is locally cross-linked")
	return nil, nil
}

// TestConnectMultiWorkloadLocalLink exercises two workload files where comp1 depends
// on comp2's "http" endpoint and both are passed to Connect - comp2's env binding
// should point straight at a local host:port (default: comp2's own declared port on
// 127.0.0.1) with no tunnel/resolve call involved.
func TestConnectMultiWorkloadLocalLink(t *testing.T) {
	comp1 := writeWorkloadFileContent(t, "comp1.yaml", consumerWorkloadYAML)
	comp2 := writeWorkloadFileContent(t, "comp2.yaml", providerWorkloadYAML)

	d := New(erroringResolver{t: t})
	d.dialStream = func(context.Context, string, string, string, string) (net.Conn, error) {
		t.Fatal("dialStream should not be called for a locally-linked dependency")
		return nil, nil
	}

	var gotEnv map[string]string
	d.runShell = func(_ context.Context, env []string) error {
		gotEnv = envToMap(env)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Connect(ctx, ConnectParams{
		WorkloadPaths: []string{comp1, comp2},
		Namespace:     "default",
		Environment:   "development",
	}, io.Discard); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if got, want := gotEnv["COMP2_URL"], "http://127.0.0.1:9091"; got != want {
		t.Errorf("COMP2_URL = %q, want %q", got, want)
	}
}

// TestConnectMultiWorkloadLocalLinkOverride exercises --local overriding the default
// local host:port for a cross-linked dependency.
func TestConnectMultiWorkloadLocalLinkOverride(t *testing.T) {
	comp1 := writeWorkloadFileContent(t, "comp1.yaml", consumerWorkloadYAML)
	comp2 := writeWorkloadFileContent(t, "comp2.yaml", providerWorkloadYAML)

	d := New(erroringResolver{t: t})

	var gotEnv map[string]string
	d.runShell = func(_ context.Context, env []string) error {
		gotEnv = envToMap(env)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := d.Connect(ctx, ConnectParams{
		WorkloadPaths:  []string{comp1, comp2},
		Namespace:      "default",
		Environment:    "development",
		LocalOverrides: map[string]LocalTarget{"comp2": {Host: "127.0.0.1", Port: 9999}},
	}, io.Discard)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if got, want := gotEnv["COMP2_URL"], "http://127.0.0.1:9999"; got != want {
		t.Errorf("COMP2_URL = %q, want %q", got, want)
	}
}

func TestBuildResolveRequestFromWorkloadFile(t *testing.T) {
	wl, err := loadWorkloadFromFile(writeWorkloadFile(t))
	if err != nil {
		t.Fatal(err)
	}
	req := buildResolveRequest(wl, "default", "development", nil)
	if req.Namespace != "default" || req.Project != "doclet" || req.Component != "doclet-document" || req.Environment != "development" {
		t.Fatalf("unexpected identity: %+v", req)
	}
	if len(req.Resources) != 1 || req.Resources[0].Ref != "doclet-postgres" {
		t.Fatalf("unexpected resources: %+v", req.Resources)
	}
	if req.Resources[0].EnvBindings["host"] != "DB_HOST" {
		t.Fatalf("unexpected env bindings: %+v", req.Resources[0].EnvBindings)
	}
}
