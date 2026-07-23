// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusteragent

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/openchoreo/openchoreo/internal/cluster-agent/messaging"
)

// dialDefaultTimeout bounds dialing a dep-connect dependency target.
const dialDefaultTimeout = 10 * time.Second

// dialSession is an active dep-connect TCP dial session: a raw byte pipe between
// the gateway (relaying occ's bytes) and the dependency target the agent dialed.
type dialSession struct {
	requestID string
	conn      net.Conn
	cancel    context.CancelFunc
	done      chan struct{}
	once      sync.Once
}

func (s *dialSession) close() {
	s.once.Do(func() {
		close(s.done)
		s.cancel()
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

// routeDialChunk delivers an inbound chunk (bytes from occ, via the gateway) to its
// dial session, if one exists for the chunk's requestID.
func (a *Agent) routeDialChunk(chunk *messaging.HTTPTunnelStreamChunk) bool {
	a.dialStreamsMu.Lock()
	session, ok := a.dialStreams[chunk.RequestID]
	a.dialStreamsMu.Unlock()

	if !ok {
		return false
	}

	if chunk.IsClose {
		session.close()
		return true
	}

	if len(chunk.Data) > 0 && session.conn != nil {
		if _, err := session.conn.Write(chunk.Data); err != nil {
			session.close()
		}
	}
	return true
}

// handleDialStreamInit opens a raw TCP connection to init.DialAddr and pipes bytes
// between it and the gateway (which relays to/from occ), framed as
// HTTPTunnelStreamChunks. Dispatched from Agent.handleConnection for Target == "tcp".
//
// The dial target is resolved and authorized entirely by the control plane before
// this message ever reaches the agent (worklog D9) — the agent trusts the gateway's
// request the same way it already trusts exec requests naming a pod to run a shell in.
func (a *Agent) handleDialStreamInit(parentCtx context.Context, init *messaging.HTTPTunnelStreamInit) {
	logger := a.logger.With("requestID", init.RequestID, "target", "tcp", "addr", init.DialAddr)
	logger.Info("Received dep-connect dial stream init")

	if init.DialAddr == "" {
		a.sendStreamClose(init.RequestID, "dialAddr is required")
		return
	}

	ctx, cancel := context.WithCancel(parentCtx)
	session := &dialSession{requestID: init.RequestID, cancel: cancel, done: make(chan struct{})}

	a.dialStreamsMu.Lock()
	if _, exists := a.dialStreams[init.RequestID]; exists {
		a.dialStreamsMu.Unlock()
		session.close()
		logger.Warn("duplicate dial stream requestID; rejecting new session")
		a.sendStreamClose(init.RequestID, "duplicate dial stream requestID")
		return
	}
	a.dialStreams[init.RequestID] = session
	a.dialStreamsMu.Unlock()

	defer func() {
		session.close()
		a.dialStreamsMu.Lock()
		delete(a.dialStreams, init.RequestID)
		a.dialStreamsMu.Unlock()
	}()

	dialCtx, dialCancel := context.WithTimeout(ctx, dialDefaultTimeout)
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", init.DialAddr)
	dialCancel()
	if err != nil {
		logger.Warn("dial upstream failed", "error", err)
		a.sendStreamClose(init.RequestID, fmt.Sprintf("dial failed: %v", err))
		return
	}
	session.conn = conn
	defer conn.Close()

	// Sentinel chunk so the gateway knows the dial succeeded and can commit to the
	// WebSocket upgrade toward occ (mirrors the hubble/exec "first chunk" convention).
	a.sendStreamChunkRaw(init.RequestID, []byte{}, 0)

	logger.Debug("dial stream connected")

	buf := make([]byte, 32*1024)
	for {
		n, rerr := conn.Read(buf)
		if n > 0 {
			if serr := a.sendStreamChunk(&messaging.HTTPTunnelStreamChunk{
				RequestID: init.RequestID,
				Data:      buf[:n],
			}); serr != nil {
				logger.Debug("failed to forward dial chunk; closing stream", "error", serr)
				break
			}
		}
		if rerr != nil {
			break
		}
	}

	logger.Debug("dial stream completed")
	a.sendStreamClose(init.RequestID, "")
}
