// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devagent

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/openchoreo/openchoreo/internal/devconnect"
)

// handleStream services one multiplexed stream: read StreamOpen, authorize the key
// against the connection's capability, dial the target, and pipe bytes both ways.
func (s *Server) handleStream(ctx context.Context, stream net.Conn, targets map[string]devconnect.Target) {
	defer stream.Close()

	_ = stream.SetReadDeadline(time.Now().Add(s.cfg.StreamOpenTimeout))
	var open devconnect.StreamOpen
	if err := devconnect.ReadMessage(stream, &open); err != nil {
		s.log.Debug("stream open read failed", "error", err)
		return
	}
	_ = stream.SetReadDeadline(time.Time{})

	target, ok := targets[open.Key]
	if !ok {
		// Not in the CP-signed target set — refuse (worklog D9: no free-form dialing).
		s.log.Warn("stream target not authorized", "key", open.Key)
		_ = devconnect.WriteMessage(stream, devconnect.StreamResult{OK: false, Error: "target not authorized"})
		return
	}

	network := target.Proto
	if network == "" {
		network = "tcp"
	}
	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))

	dialCtx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout)
	defer cancel()
	upstream, err := s.dialer(dialCtx, network, addr)
	if err != nil {
		s.log.Warn("dial upstream failed", "key", open.Key, "addr", addr, "error", err)
		_ = devconnect.WriteMessage(stream, devconnect.StreamResult{OK: false, Error: "dial failed"})
		return
	}
	defer upstream.Close()

	if err := devconnect.WriteMessage(stream, devconnect.StreamResult{OK: true}); err != nil {
		s.log.Debug("stream result write failed", "key", open.Key, "error", err)
		return
	}
	s.log.Debug("stream connected", "key", open.Key, "addr", addr)
	devconnect.Pipe(stream, upstream)
}
