// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openchoreo/openchoreo/internal/cmdutil"
	devagent "github.com/openchoreo/openchoreo/internal/dev-agent"
)

const (
	defaultHandshakeTimeout  = 10 * time.Second
	defaultStreamOpenTimeout = 10 * time.Second
	defaultDialTimeout       = 10 * time.Second
)

func main() {
	var (
		listenAddr        string
		planeID           string
		tlsCertPath       string
		tlsKeyPath        string
		capPubKeyPath     string
		handshakeTimeout  time.Duration
		streamOpenTimeout time.Duration
		dialTimeout       time.Duration
		maxStreams        int
		logLevel          string
	)

	flag.StringVar(&listenAddr, "listen", cmdutil.GetEnv("LISTEN_ADDR", ":8443"),
		"TLS tunnel listen address")
	flag.StringVar(&planeID, "plane-id", cmdutil.GetEnv("PLANE_ID", ""),
		"Logical plane identifier; capabilities must target dev-agent:<plane-id>")
	flag.StringVar(&tlsCertPath, "tls-cert", cmdutil.GetEnv("TLS_CERT_PATH", "/certs/tls.crt"),
		"Path to the server TLS certificate")
	flag.StringVar(&tlsKeyPath, "tls-key", cmdutil.GetEnv("TLS_KEY_PATH", "/certs/tls.key"),
		"Path to the server TLS private key")
	flag.StringVar(&capPubKeyPath, "capability-pubkey",
		cmdutil.GetEnv("CAPABILITY_PUBKEY_PATH", "/keys/capability-pub.pem"),
		"Path to the PEM Ed25519 public key used to verify control-plane capabilities")
	flag.DurationVar(&handshakeTimeout, "handshake-timeout", defaultHandshakeTimeout,
		"Timeout for the capability handshake")
	flag.DurationVar(&streamOpenTimeout, "stream-open-timeout", defaultStreamOpenTimeout,
		"Timeout for a client to send StreamOpen after opening a stream")
	flag.DurationVar(&dialTimeout, "dial-timeout", defaultDialTimeout,
		"Timeout for dialing an upstream dependency target")
	flag.IntVar(&maxStreams, "max-streams-per-session",
		cmdutil.GetEnvInt("MAX_STREAMS_PER_SESSION", 256),
		"Maximum concurrent streams per tunnel connection (0 = unlimited)")
	flag.StringVar(&logLevel, "log-level", cmdutil.GetEnv("LOG_LEVEL", "info"),
		"Log level (debug, info, warn, error)")
	flag.Parse()

	if planeID == "" {
		fmt.Println("Error: --plane-id is required")
		flag.Usage()
		os.Exit(1)
	}

	logger := cmdutil.SetupLogger(logLevel)

	verifyKey, err := devagent.LoadEd25519PublicKeyPEM(capPubKeyPath)
	if err != nil {
		logger.Error("failed to load capability public key", "path", capPubKeyPath, "error", err)
		os.Exit(1)
	}

	cfg := devagent.Config{
		ListenAddr:           listenAddr,
		PlaneID:              planeID,
		TLSCertPath:          tlsCertPath,
		TLSKeyPath:           tlsKeyPath,
		CapabilityPubKeyPath: capPubKeyPath,
		HandshakeTimeout:     handshakeTimeout,
		StreamOpenTimeout:    streamOpenTimeout,
		DialTimeout:          dialTimeout,
		MaxStreamsPerSession: maxStreams,
	}

	logger.Info("starting OpenChoreo dev-tunnel agent",
		"listen", listenAddr, "planeID", planeID, "tlsCert", tlsCertPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := devagent.NewServer(cfg, verifyKey, logger)
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("dev-agent failed", "error", err)
		os.Exit(1)
	}
	logger.Info("dev-agent shutdown completed")
}
