// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package devagent implements the OpenChoreo dev-tunnel agent: a data-plane
// component that terminates TLS tunnels from `occ dev connect`, verifies the
// control-plane-signed capability, and forwards each multiplexed stream to a
// pre-authorized dependency service (worklog.md §8.3).
package devagent

import "time"

// Config configures the dev-tunnel agent server.
type Config struct {
	// ListenAddr is the TCP address the TLS tunnel listener binds to (e.g. ":8443").
	ListenAddr string

	// PlaneID identifies the plane this agent serves. Capabilities must carry the
	// matching audience (devconnect.AgentAudience(PlaneID)).
	PlaneID string

	// TLSCertPath / TLSKeyPath are the server certificate and key presented to occ.
	TLSCertPath string
	TLSKeyPath  string

	// CapabilityPubKeyPath is the PEM-encoded Ed25519 public key used to verify the
	// control plane's signed capabilities. (v1: a mounted key; JWKS is a follow-up.)
	CapabilityPubKeyPath string

	// HandshakeTimeout bounds how long the Hello/HelloResult exchange may take.
	HandshakeTimeout time.Duration

	// StreamOpenTimeout bounds how long a client may take to send StreamOpen after
	// opening a yamux stream.
	StreamOpenTimeout time.Duration

	// DialTimeout bounds dialing an upstream dependency target.
	DialTimeout time.Duration

	// MaxStreamsPerSession caps concurrent streams on a single tunnel connection
	// (0 = unlimited).
	MaxStreamsPerSession int
}

// Default timeouts / limits.
const (
	DefaultHandshakeTimeout  = 10 * time.Second
	DefaultStreamOpenTimeout = 10 * time.Second
	DefaultDialTimeout       = 10 * time.Second
)

// withDefaults returns a copy of the config with zero-valued timeouts filled in.
func (c Config) withDefaults() Config {
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if c.StreamOpenTimeout == 0 {
		c.StreamOpenTimeout = DefaultStreamOpenTimeout
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	return c
}
