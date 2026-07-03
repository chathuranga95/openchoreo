// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devagent

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/openchoreo/openchoreo/internal/devconnect"
)

// Server terminates dev-connect tunnels and forwards multiplexed streams to the
// dependency targets authorized by each connection's capability.
type Server struct {
	cfg       Config
	audience  string
	verifyKey ed25519.PublicKey
	log       *slog.Logger

	// dialer dials upstream dependency targets. Overridable in tests.
	dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewServer builds a Server that verifies capabilities with verifyKey.
func NewServer(cfg Config, verifyKey ed25519.PublicKey, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg:       cfg.withDefaults(),
		audience:  devconnect.AgentAudience(cfg.PlaneID),
		verifyKey: verifyKey,
		log:       log,
		dialer:    (&net.Dialer{}).DialContext,
	}
}

// Run binds a TLS listener from the configured cert/key and serves until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	tlsCfg, err := serverTLSConfig(s.cfg.TLSCertPath, s.cfg.TLSKeyPath)
	if err != nil {
		return err
	}
	ln, err := tls.Listen("tcp", s.cfg.ListenAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("dev-agent: listen on %s: %w", s.cfg.ListenAddr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve accepts tunnel connections on ln until ctx is done. Exposed for tests, which
// can pass a plain (non-TLS) listener since the protocol is transport-agnostic.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	s.log.Info("dev-agent listening", "addr", ln.Addr().String(), "planeID", s.cfg.PlaneID)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // graceful shutdown
			}
			return fmt.Errorf("dev-agent: accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

// handleConn runs the handshake for one tunnel connection, then multiplexes streams.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()

	claims, ok := s.handshake(conn, remote)
	if !ok {
		return
	}

	targets := make(map[string]devconnect.Target, len(claims.Targets))
	for _, t := range claims.Targets {
		targets[t.Key] = t
	}
	s.log.Info("tunnel established",
		"remote", remote, "subject", claims.Subject,
		"component", claims.Component.Name, "env", claims.Env, "targets", len(targets))

	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard // quiet yamux; our logging is structured
	session, err := yamux.Server(conn, ycfg)
	if err != nil {
		s.log.Warn("yamux session setup failed", "remote", remote, "error", err)
		return
	}
	defer session.Close()

	var sem chan struct{}
	if s.cfg.MaxStreamsPerSession > 0 {
		sem = make(chan struct{}, s.cfg.MaxStreamsPerSession)
	}
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return // session closed or client gone
		}
		if sem != nil {
			select {
			case sem <- struct{}{}:
			default:
				go s.rejectStream(stream, "too many concurrent streams")
				continue
			}
		}
		go func(st net.Conn) {
			if sem != nil {
				defer func() { <-sem }()
			}
			s.handleStream(ctx, st, targets)
		}(stream)
	}
}

// handshake reads Hello, verifies the capability, and replies HelloResult. It returns
// the verified claims on success.
func (s *Server) handshake(conn net.Conn, remote string) (*devconnect.CapabilityClaims, bool) {
	_ = conn.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	var hello devconnect.Hello
	if err := devconnect.ReadMessage(conn, &hello); err != nil {
		s.log.Warn("handshake read failed", "remote", remote, "error", err)
		return nil, false
	}
	if hello.ProtocolVersion != devconnect.ProtocolVersion {
		_ = devconnect.WriteMessage(conn, devconnect.HelloResult{OK: false, Error: "unsupported protocol version"})
		return nil, false
	}
	claims, err := devconnect.VerifyCapability(hello.Capability, s.verifyKey, s.audience)
	if err != nil {
		s.log.Warn("capability rejected", "remote", remote, "error", err)
		_ = devconnect.WriteMessage(conn, devconnect.HelloResult{OK: false, Error: "invalid capability"})
		return nil, false
	}
	if err := devconnect.WriteMessage(conn, devconnect.HelloResult{OK: true}); err != nil {
		s.log.Warn("handshake reply failed", "remote", remote, "error", err)
		return nil, false
	}
	return claims, true
}

func (s *Server) rejectStream(stream net.Conn, reason string) {
	defer stream.Close()
	_ = devconnect.WriteMessage(stream, devconnect.StreamResult{OK: false, Error: reason})
}

func serverTLSConfig(certPath, keyPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("dev-agent: load TLS keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// LoadEd25519PublicKeyPEM loads a PKIX PEM-encoded Ed25519 public key used to verify
// capabilities.
func LoadEd25519PublicKeyPEM(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("dev-agent: no PEM block in capability public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("dev-agent: parse capability public key: %w", err)
	}
	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("dev-agent: capability key is %T, want ed25519", pub)
	}
	return edPub, nil
}
