// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package depconnect

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// UpgradeProtocol is the Upgrade header value both hops use to open a dep-connect
// raw TCP tunnel. Unlike WebSocket, there is no message framing: once the upgrade
// completes, the connection is a transparent byte pipe carrying whatever protocol
// the tunnelled dependency speaks (HTTP, Postgres wire protocol, Redis, ...).
const UpgradeProtocol = "depconnect-tcp"

// IsUpgradeRequest reports whether r is asking to switch to the dep-connect raw TCP
// protocol.
func IsUpgradeRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), UpgradeProtocol)
}

// bufConn wraps a connection whose HTTP layer (request or response parsing) may
// have buffered bytes past the header boundary, so reads are never lost: they are
// served from the buffered reader first, then fall through to the raw connection.
// Writes go straight to the connection.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// CompleteUpgrade hijacks w's underlying connection and writes a "101 Switching
// Protocols" response, committing the request to a raw byte pipe. Call this only
// once the caller already knows the tunnel will succeed end to end — on failure,
// use http.Error instead, which needs no hijack and lets the client read an
// ordinary HTTP error status.
func CompleteUpgrade(w http.ResponseWriter) (net.Conn, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("depconnect: response writer does not support hijacking")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("depconnect: hijack: %w", err)
	}
	if err := brw.Writer.Flush(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("depconnect: flush before upgrade: %w", err)
	}
	if _, err := io.WriteString(conn,
		"HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+UpgradeProtocol+"\r\n\r\n"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("depconnect: write upgrade response: %w", err)
	}
	return &bufConn{Conn: conn, r: brw.Reader}, nil
}

// UpgradeError reports a failed upgrade handshake with the upstream's HTTP status,
// so callers can propagate a matching status to their own caller.
type UpgradeError struct {
	StatusCode int
	Message    string
}

func (e *UpgradeError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("depconnect: upgrade failed (HTTP %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("depconnect: upgrade failed (HTTP %d)", e.StatusCode)
}

// DialUpgrade dials target (an http:// or https:// URL) and performs a raw HTTP
// upgrade handshake with header attached to the request. On a 101 response it
// returns a raw byte pipe to the connection; on any other response it reads a short
// error body and returns an *UpgradeError. tlsConfig is used for https targets and
// may be nil (system roots).
func DialUpgrade(ctx context.Context, target string, header http.Header, tlsConfig *tls.Config) (net.Conn, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("depconnect: parse upgrade URL: %w", err)
	}

	dialAddr := u.Host
	if _, _, serr := net.SplitHostPort(dialAddr); serr != nil {
		if u.Scheme == "https" {
			dialAddr = net.JoinHostPort(dialAddr, "443")
		} else {
			dialAddr = net.JoinHostPort(dialAddr, "80")
		}
	}

	rawConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", dialAddr)
	if err != nil {
		return nil, fmt.Errorf("depconnect: dial %s: %w", dialAddr, err)
	}

	conn := rawConn
	if u.Scheme == "https" {
		cfg := tlsConfig
		if cfg == nil {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if cfg.ServerName == "" && !cfg.InsecureSkipVerify {
			cfg = cfg.Clone()
			cfg.ServerName = u.Hostname()
		}
		tlsConn := tls.Client(rawConn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("depconnect: TLS handshake with %s: %w", dialAddr, err)
		}
		conn = tlsConn
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("depconnect: build upgrade request: %w", err)
	}
	if header != nil {
		req.Header = header.Clone()
	} else {
		req.Header = http.Header{}
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", UpgradeProtocol)

	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("depconnect: write upgrade request: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("depconnect: read upgrade response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = conn.Close()
		return nil, &UpgradeError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(msg))}
	}

	return &bufConn{Conn: conn, r: br}, nil
}
