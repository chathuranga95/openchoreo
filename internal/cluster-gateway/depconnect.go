// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clustergateway

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/openchoreo/openchoreo/internal/cluster-agent/messaging"
	"github.com/openchoreo/openchoreo/internal/depconnect"
)

// handleDepConnect relays a single dep-connect TCP stream (one accepted local
// connection, forwarded by occ via openchoreo-api) to the dependency target the
// control plane resolved, through the existing per-CR cluster-agent connection.
//
// URL: /api/depconnect/{planeType}/{planeID}/{crNamespace}/{crName}?host=...&port=...
//
// Unlike handleExec, this endpoint commits to the HTTP Upgrade only after the agent
// has confirmed the dial succeeded (mirroring handleWirelogs' delayed-commit
// pattern), so failures (no agent, dial refused) surface as an ordinary HTTP error
// status rather than an upgrade followed by a silent close.
func (s *Server) handleDepConnect(w http.ResponseWriter, r *http.Request) {
	requestID := getOrGenerateRequestID(r)
	logger := s.logger.With("requestId", requestID)

	if !depconnect.IsUpgradeRequest(r) {
		http.Error(w, "expected a depconnect-tcp upgrade request", http.StatusBadRequest)
		return
	}

	// Parse URL: /api/depconnect/{planeType}/{planeID}/{crNamespace}/{crName}
	path := strings.TrimPrefix(r.URL.Path, "/api/depconnect/")
	parts := strings.SplitN(path, "/", 4)
	if len(parts) < 4 {
		http.Error(w, "invalid depconnect URL: expected /api/depconnect/{planeType}/{planeID}/{crNamespace}/{crName}",
			http.StatusBadRequest)
		return
	}
	planeType := parts[0]
	planeID := parts[1]
	crNamespace := parts[2]
	crName := parts[3]

	query := r.URL.Query()
	host := query.Get("host")
	port := query.Get("port")
	if host == "" || port == "" {
		http.Error(w, "host and port query parameters are required", http.StatusBadRequest)
		return
	}

	planeIdentifier := fmt.Sprintf("%s/%s", planeType, planeID)
	if crNamespace == crNamespaceClusterPlaceholder {
		crNamespace = ""
	}
	crKey := fmt.Sprintf("%s/%s", crNamespace, crName)

	logger.Info("Dep-connect stream request received",
		"plane", planeIdentifier, "cr", crKey, "host", host, "port", port)

	agentConn, err := s.connMgr.GetForCR(planeIdentifier, crKey)
	if err != nil {
		logger.Warn("No agent available for dep-connect", "error", err)
		http.Error(w, fmt.Sprintf("no agent available: %v", err), http.StatusServiceUnavailable)
		return
	}

	session := &streamSession{
		requestID: requestID,
		fromAgent: make(chan *messaging.HTTPTunnelStreamChunk, 256),
		done:      make(chan struct{}),
	}
	s.registerStreamSession(requestID, session)
	defer s.unregisterStreamSession(requestID)

	streamInit := &messaging.HTTPTunnelStreamInit{
		RequestID:    requestID,
		Target:       "tcp",
		DialAddr:     net.JoinHostPort(host, port),
		IsUpgrade:    true,
		UpgradeProto: depconnect.UpgradeProtocol,
	}
	initData, err := json.Marshal(streamInit)
	if err != nil {
		logger.Error("Failed to marshal stream init", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := agentConn.SendRawMessage(initData); err != nil {
		logger.Error("Failed to send stream init to agent", "error", err)
		http.Error(w, fmt.Sprintf("failed to start stream: %v", err), http.StatusBadGateway)
		return
	}

	// Wait for the agent's dial ack before upgrading, so a dial failure surfaces as
	// a normal HTTP error rather than an upgrade followed by a silent close.
	select {
	case chunk := <-session.fromAgent:
		if chunk == nil {
			http.Error(w, "stream closed before start", http.StatusBadGateway)
			return
		}
		if chunk.IsClose {
			logger.Warn("Agent refused dep-connect dial", "reason", string(chunk.Data))
			http.Error(w, fmt.Sprintf("dial failed: %s", string(chunk.Data)), http.StatusBadGateway)
			return
		}
	case <-time.After(30 * time.Second):
		logger.Error("Timeout waiting for agent to dial dep-connect target")
		http.Error(w, "timeout waiting for agent", http.StatusGatewayTimeout)
		return
	case <-r.Context().Done():
		return
	}

	rawConn, err := depconnect.CompleteUpgrade(w)
	if err != nil {
		logger.Error("Failed to complete depconnect upgrade", "error", err)
		return
	}
	defer rawConn.Close()

	logger.Info("Dep-connect stream established")

	// caller (openchoreo-api) → agent
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := rawConn.Read(buf)
			if n > 0 {
				data, merr := json.Marshal(&messaging.HTTPTunnelStreamChunk{
					RequestID: requestID,
					Data:      buf[:n],
				})
				if merr == nil {
					if serr := agentConn.SendRawMessage(data); serr != nil {
						break
					}
				}
			}
			if rerr != nil {
				break
			}
		}
		closeChunk, _ := json.Marshal(&messaging.HTTPTunnelStreamChunk{RequestID: requestID, IsClose: true})
		_ = agentConn.SendRawMessage(closeChunk)
		session.close()
	}()

	// agent → caller
	for {
		select {
		case chunk, ok := <-session.fromAgent:
			if !ok || chunk == nil {
				return
			}
			if chunk.IsClose {
				return
			}
			if len(chunk.Data) > 0 {
				if _, werr := rawConn.Write(chunk.Data); werr != nil {
					return
				}
			}
		case <-session.done:
			return
		}
	}
}
