// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clustergateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openchoreo/openchoreo/internal/cluster-agent/messaging"
)

// handleL4 proxies a raw-TCP (L4) tunnel between the caller (openchoreo-api,
// ultimately occ) and an in-cluster target dialed by the agent. It mirrors
// handleExec but carries undifferentiated bytes: no SPDY upgrade, no stream
// typing. Inbound agent chunks are delivered by the shared handleStreamChunk
// router via the streamSession registry, so no new routing is needed here.
//
// URL: /api/l4/{planeType}/{planeID}/{crNamespace}/{crName}?target=host:port
func (s *Server) handleL4(w http.ResponseWriter, r *http.Request) {
	requestID := getOrGenerateRequestID(r)
	logger := s.logger.With("requestId", requestID)

	path := strings.TrimPrefix(r.URL.Path, "/api/l4/")
	parts := strings.SplitN(path, "/", 4)
	if len(parts) < 4 {
		http.Error(w, "invalid l4 URL: expected /api/l4/{planeType}/{planeID}/{crNamespace}/{crName}", http.StatusBadRequest)
		return
	}
	planeType := parts[0]
	planeID := parts[1]
	crNamespace := parts[2]
	crName := parts[3]

	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "target query parameter (host:port) is required", http.StatusBadRequest)
		return
	}

	planeIdentifier := fmt.Sprintf("%s/%s", planeType, planeID)
	if crNamespace == crNamespaceClusterPlaceholder {
		crNamespace = ""
	}
	crKey := fmt.Sprintf("%s/%s", crNamespace, crName)

	logger.Info("L4 tunnel request received", "plane", planeIdentifier, "cr", crKey, "target", target)

	// Verify an agent connection exists for the plane/CR before upgrading.
	conn, err := s.connMgr.GetForCR(planeIdentifier, crKey)
	if err != nil {
		logger.Warn("No agent available for L4 tunnel", "error", err)
		http.Error(w, fmt.Sprintf("no agent available: %v", err), http.StatusServiceUnavailable)
		return
	}

	apiConn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Failed to upgrade L4 to WebSocket", "error", err)
		return
	}
	defer apiConn.Close()

	session := &streamSession{
		requestID: requestID,
		fromAgent: make(chan *messaging.HTTPTunnelStreamChunk, 256),
		done:      make(chan struct{}),
	}
	s.registerStreamSession(requestID, session)
	defer s.unregisterStreamSession(requestID)

	streamInit := &messaging.HTTPTunnelStreamInit{
		RequestID:    requestID,
		Target:       "l4",
		Path:         target,
		IsUpgrade:    true,
		UpgradeProto: "tcp",
	}
	initData, err := json.Marshal(streamInit)
	if err != nil {
		logger.Error("Failed to marshal L4 stream init", "error", err)
		return
	}
	if err := conn.SendRawMessage(initData); err != nil {
		logger.Error("Failed to send L4 stream init to agent", "error", err)
		return
	}
	logger.Info("L4 stream init sent to agent")

	// Wait for the agent's readiness signal (first chunk). A close instead
	// means the dial failed; abort without starting the client pump so no
	// client bytes are sent into a dead stream.
	select {
	case chunk := <-session.fromAgent:
		if chunk == nil || chunk.IsClose {
			logger.Warn("Agent closed L4 stream before it started", "data", string(chunkData(chunk)))
			return
		}
	case <-time.After(30 * time.Second):
		logger.Error("Timeout waiting for agent to establish L4 connection")
		return
	case <-session.done:
		return
	}

	// client (api) -> agent
	go func() {
		defer session.close()
		for {
			_, msg, rerr := apiConn.ReadMessage()
			if rerr != nil {
				closeChunk, _ := json.Marshal(&messaging.HTTPTunnelStreamChunk{
					RequestID: requestID,
					IsClose:   true,
				})
				_ = conn.SendRawMessage(closeChunk)
				return
			}
			chunkData, merr := json.Marshal(&messaging.HTTPTunnelStreamChunk{
				RequestID: requestID,
				Data:      msg,
			})
			if merr != nil {
				return
			}
			if serr := conn.SendRawMessage(chunkData); serr != nil {
				return
			}
		}
	}()

	// agent -> client (api)
	for {
		select {
		case chunk, ok := <-session.fromAgent:
			if !ok || chunk == nil {
				return
			}
			if chunk.IsClose {
				_ = apiConn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if len(chunk.Data) > 0 {
				if werr := apiConn.WriteMessage(websocket.BinaryMessage, chunk.Data); werr != nil {
					return
				}
			}
		case <-session.done:
			return
		}
	}
}

// chunkData returns a chunk's data defensively for logging (nil-safe).
func chunkData(c *messaging.HTTPTunnelStreamChunk) []byte {
	if c == nil {
		return nil
	}
	return c.Data
}
