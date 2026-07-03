// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devagent

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hashicorp/yamux"

	"github.com/openchoreo/openchoreo/internal/devconnect"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startEchoServer starts a TCP echo server standing in for a dependency service.
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

// startAgent serves a dev-agent on a plain (non-TLS) listener for the test.
func startAgent(t *testing.T, cfg Config, pub ed25519.PublicKey) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg, pub, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, ln) }()
	return ln.Addr().String()
}

func signCap(t *testing.T, priv ed25519.PrivateKey, planeID string, targets []devconnect.Target) string {
	t.Helper()
	claims := &devconnect.CapabilityClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user:test",
			Audience:  jwt.ClaimStrings{devconnect.AgentAudience(planeID)},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
		},
		Component: devconnect.ComponentRef{Project: "p", Name: "c"},
		Env:       "development",
		Targets:   targets,
	}
	tok, err := devconnect.SignCapability(claims, priv, "k1")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// dialTunnel performs the client handshake and returns a yamux session.
func dialTunnel(t *testing.T, addr, capToken string) *yamux.Session {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := devconnect.WriteMessage(conn, devconnect.Hello{
		ProtocolVersion: devconnect.ProtocolVersion,
		Capability:      capToken,
	}); err != nil {
		t.Fatal(err)
	}
	var res devconnect.HelloResult
	if err := devconnect.ReadMessage(conn, &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("handshake rejected: %s", res.Error)
	}
	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	sess, err := yamux.Client(conn, ycfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func TestTunnelEchoAuthorized(t *testing.T) {
	host, port := startEchoServer(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	agentAddr := startAgent(t, Config{PlaneID: "dp-1"}, pub)

	capToken := signCap(t, priv, "dp-1", []devconnect.Target{
		{Key: "echo", Proto: "tcp", Host: host, Port: port},
	})
	sess := dialTunnel(t, agentAddr, capToken)

	stream, err := sess.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := devconnect.WriteMessage(stream, devconnect.StreamOpen{Key: "echo"}); err != nil {
		t.Fatal(err)
	}
	var sr devconnect.StreamResult
	if err := devconnect.ReadMessage(stream, &sr); err != nil {
		t.Fatal(err)
	}
	if !sr.OK {
		t.Fatalf("stream open rejected: %s", sr.Error)
	}

	msg := []byte("ping-123")
	if _, err := stream.Write(msg); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", got, msg)
	}
}

func TestTunnelUnauthorizedTargetRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	agentAddr := startAgent(t, Config{PlaneID: "dp-1"}, pub)

	// Capability authorizes "echo" only; the client asks for a different key.
	capToken := signCap(t, priv, "dp-1", []devconnect.Target{
		{Key: "echo", Proto: "tcp", Host: "127.0.0.1", Port: 1},
	})
	sess := dialTunnel(t, agentAddr, capToken)

	stream, err := sess.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := devconnect.WriteMessage(stream, devconnect.StreamOpen{Key: "not-allowed"}); err != nil {
		t.Fatal(err)
	}
	var sr devconnect.StreamResult
	if err := devconnect.ReadMessage(stream, &sr); err != nil {
		t.Fatal(err)
	}
	if sr.OK {
		t.Fatal("expected unauthorized target to be rejected")
	}
}

func TestHandshakeRejectsBadCapability(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	agentAddr := startAgent(t, Config{PlaneID: "dp-1"}, pub)

	conn, err := net.Dial("tcp", agentAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := devconnect.WriteMessage(conn, devconnect.Hello{
		ProtocolVersion: devconnect.ProtocolVersion,
		Capability:      "not.a.valid.jwt",
	}); err != nil {
		t.Fatal(err)
	}
	var res devconnect.HelloResult
	if err := devconnect.ReadMessage(conn, &res); err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected bad capability to be rejected at handshake")
	}
}
