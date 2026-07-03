// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devconnect

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/hashicorp/yamux"
)

// TunnelClient is the occ-side endpoint of a dev-connect tunnel: it performs the
// capability handshake over a connection and multiplexes per-target streams with
// yamux.
type TunnelClient struct {
	conn    net.Conn
	session *yamux.Session
}

// NewTunnelClient runs the Hello/HelloResult handshake over an already-established
// connection (TLS in production; plain in tests) and layers a yamux client session.
func NewTunnelClient(conn net.Conn, capability string) (*TunnelClient, error) {
	if err := WriteMessage(conn, Hello{ProtocolVersion: ProtocolVersion, Capability: capability}); err != nil {
		return nil, fmt.Errorf("devconnect: send hello: %w", err)
	}
	var res HelloResult
	if err := ReadMessage(conn, &res); err != nil {
		return nil, fmt.Errorf("devconnect: read hello result: %w", err)
	}
	if !res.OK {
		return nil, fmt.Errorf("devconnect: tunnel handshake rejected: %s", res.Error)
	}

	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard // quiet yamux; occ surfaces its own errors
	session, err := yamux.Client(conn, ycfg)
	if err != nil {
		return nil, fmt.Errorf("devconnect: yamux client: %w", err)
	}
	return &TunnelClient{conn: conn, session: session}, nil
}

// DialTLS dials endpoint over TLS (optionally pinning caBundle) and constructs a
// TunnelClient. If caBundle is empty, the system roots are used.
func DialTLS(endpoint, caBundle, capability string) (*TunnelClient, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caBundle != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caBundle)) {
			return nil, errors.New("devconnect: invalid agent CA bundle")
		}
		tlsCfg.RootCAs = pool
	}
	conn, err := tls.Dial("tcp", endpoint, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("devconnect: dial agent %s: %w", endpoint, err)
	}
	c, err := NewTunnelClient(conn, capability)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// OpenStream opens a multiplexed stream and requests the target identified by key.
// The returned net.Conn is a transparent byte pipe to the dialed dependency.
func (c *TunnelClient) OpenStream(key string) (net.Conn, error) {
	stream, err := c.session.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("devconnect: open stream: %w", err)
	}
	if err := WriteMessage(stream, StreamOpen{Key: key}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	var res StreamResult
	if err := ReadMessage(stream, &res); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if !res.OK {
		_ = stream.Close()
		return nil, fmt.Errorf("devconnect: stream to %q rejected: %s", key, res.Error)
	}
	return stream, nil
}

// Close tears down the session and underlying connection.
func (c *TunnelClient) Close() error {
	if c.session != nil {
		_ = c.session.Close()
	}
	return c.conn.Close()
}
