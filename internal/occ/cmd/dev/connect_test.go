// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package dev

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	devagent "github.com/openchoreo/openchoreo/internal/dev-agent"
	"github.com/openchoreo/openchoreo/internal/devconnect"
)

type fakeResolver struct{ resp *devconnect.ResolveResponse }

func (f *fakeResolver) Resolve(context.Context, devconnect.ResolveRequest) (*devconnect.ResolveResponse, error) {
	return f.resp, nil
}

func startEchoServer(t *testing.T) (host string, port int) {
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
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(p)
	return h, pn
}

func startAgent(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := devagent.NewServer(devagent.Config{PlaneID: "dp-1"}, pub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, ln) }()
	return ln.Addr().String()
}

func signCap(t *testing.T, priv ed25519.PrivateKey, targets []devconnect.Target) string {
	t.Helper()
	tok, err := devconnect.SignCapability(&devconnect.CapabilityClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user:test",
			Audience:  jwt.ClaimStrings{devconnect.AgentAudience("dp-1")},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
		},
		Component: devconnect.ComponentRef{Project: "doclet", Name: "doclet-document"},
		Env:       "development",
		Targets:   targets,
	}, priv, "k1")
	if err != nil {
		t.Fatal(err)
	}
	return tok
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

func TestConnectEndToEnd(t *testing.T) {
	echoHost, echoPort := startEchoServer(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	agentAddr := startAgent(t, pub)

	capToken := signCap(t, priv, []devconnect.Target{
		{Key: "res/doclet-postgres", Proto: "tcp", Host: echoHost, Port: echoPort},
	})
	resp := &devconnect.ResolveResponse{
		Agent:      devconnect.AgentEndpoint{Endpoint: agentAddr},
		Capability: capToken,
		Targets: []devconnect.ResolvedTarget{{
			Key:   "res/doclet-postgres",
			Proto: "tcp",
			Resource: &devconnect.ResourceRender{
				HostEnv:   "DB_HOST",
				PortEnv:   "DB_PORT",
				StaticEnv: map[string]string{"DB_NAME": "doclet"},
			},
		}},
	}

	d := New(&fakeResolver{resp: resp})
	// Plain TCP dial: the test agent is not TLS.
	d.dialTunnel = func(a devconnect.AgentEndpoint, capability string) (*devconnect.TunnelClient, error) {
		conn, err := net.Dial("tcp", a.Endpoint)
		if err != nil {
			return nil, err
		}
		return devconnect.NewTunnelClient(conn, capability)
	}

	var gotEnv map[string]string
	roundTripErr := make(chan error, 1)
	d.runShell = func(_ context.Context, env []string) error {
		gotEnv = envToMap(env)
		// Reach the dependency through the tunnel using the injected env.
		roundTripErr <- tunnelRoundTrip(net.JoinHostPort(gotEnv["DB_HOST"], gotEnv["DB_PORT"]), "hello-tunnel")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Connect(ctx, ConnectParams{WorkloadPath: writeWorkloadFile(t), Namespace: "default", Environment: "development"}, io.Discard); err != nil {
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

func TestBuildResolveRequestFromWorkloadFile(t *testing.T) {
	wl, err := loadWorkloadFromFile(writeWorkloadFile(t))
	if err != nil {
		t.Fatal(err)
	}
	req := buildResolveRequest(wl, "default", "development")
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
