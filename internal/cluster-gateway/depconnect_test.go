// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clustergateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openchoreo/openchoreo/internal/cluster-agent/messaging"
	"github.com/openchoreo/openchoreo/internal/depconnect"
)

// fakeDepConnectAgentConn implements the gateway's Connection interface (the shape
// ConnectionManager.Register expects). Its onInit hook lets a test inject
// dial-ack/data/close chunks back into the server's stream session, mimicking what
// the real cluster-agent would do over the persistent management tunnel.
type fakeDepConnectAgentConn struct {
	mu       sync.Mutex
	sent     [][]byte
	sendErr  error
	onInit   func(initData []byte)
	initOnce atomic.Bool
}

func (f *fakeDepConnectAgentConn) ReadMessage() (int, []byte, error) {
	return 0, nil, errors.New("fakeDepConnectAgentConn: ReadMessage not used in this seam")
}

func (f *fakeDepConnectAgentConn) WriteMessage(_ int, data []byte) error {
	f.mu.Lock()
	f.sent = append(f.sent, append([]byte(nil), data...))
	hook := f.onInit
	err := f.sendErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if hook != nil && f.initOnce.CompareAndSwap(false, true) {
		hook(data)
	}
	return nil
}

func (f *fakeDepConnectAgentConn) WriteControl(int, []byte, time.Time) error { return nil }
func (f *fakeDepConnectAgentConn) SetReadDeadline(time.Time) error           { return nil }
func (f *fakeDepConnectAgentConn) SetPongHandler(func(string) error)         {}
func (f *fakeDepConnectAgentConn) Close() error                              { return nil }

// newDepConnectTestServer builds a Server with a real ConnectionManager and, if
// fakeConn is non-nil, registers it as the sole agent connection authorized for crKey.
func newDepConnectTestServer(t *testing.T, fakeConn Connection, crKey string) *Server {
	t.Helper()
	s := &Server{
		connMgr:               NewConnectionManager(slog.New(slog.NewTextHandler(io.Discard, nil))),
		pendingStreamSessions: make(map[string]*streamSession),
		logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if fakeConn != nil {
		_, err := s.connMgr.Register("dataplane", "p1", fakeConn, []string{crKey}, nil)
		require.NoError(t, err)
	}
	return s
}

func newDepConnectRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Upgrade", depconnect.UpgradeProtocol)
	return req
}

func TestHandleDepConnect_NotAnUpgradeRequest(t *testing.T) {
	s := newDepConnectTestServer(t, nil, "")
	req := httptest.NewRequest(http.MethodGet, "/api/depconnect/dataplane/p1/ns1/cr1?host=h&port=1", nil)
	rec := httptest.NewRecorder()

	s.handleDepConnect(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "depconnect-tcp upgrade")
}

func TestHandleDepConnect_InvalidURL(t *testing.T) {
	s := newDepConnectTestServer(t, nil, "")
	req := newDepConnectRequest("/api/depconnect/dataplane/p1?host=h&port=1")
	rec := httptest.NewRecorder()

	s.handleDepConnect(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid depconnect URL")
}

func TestHandleDepConnect_MissingHostPort(t *testing.T) {
	s := newDepConnectTestServer(t, nil, "")
	req := newDepConnectRequest("/api/depconnect/dataplane/p1/ns1/cr1")
	rec := httptest.NewRecorder()

	s.handleDepConnect(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "host and port")
}

func TestHandleDepConnect_NoAgentAvailable(t *testing.T) {
	s := newDepConnectTestServer(t, nil, "")
	req := newDepConnectRequest("/api/depconnect/dataplane/p1/ns1/cr1?host=10.0.0.5&port=5432")
	rec := httptest.NewRecorder()

	s.handleDepConnect(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "no agent available")
}

func TestHandleDepConnect_AgentRefusesDial(t *testing.T) {
	fake := &fakeDepConnectAgentConn{}
	s := newDepConnectTestServer(t, fake, "ns1/cr1")
	fake.onInit = func(initData []byte) {
		var init messaging.HTTPTunnelStreamInit
		require.NoError(t, json.Unmarshal(initData, &init))
		s.handleStreamChunk(&messaging.HTTPTunnelStreamChunk{
			RequestID: init.RequestID, IsClose: true, Data: []byte("dial failed: connection refused"),
		})
	}

	req := newDepConnectRequest("/api/depconnect/dataplane/p1/ns1/cr1?host=10.0.0.5&port=5432")
	rec := httptest.NewRecorder()

	s.handleDepConnect(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "connection refused")
}

// TestHandleDepConnect_HappyPath drives the handler through a real net/http server
// (needed for http.Hijacker) using the same depconnect.DialUpgrade/CompleteUpgrade
// helpers the production occ<->openchoreo-api and openchoreo-api<->gateway hops use,
// verifying the full dial-ack-then-raw-pipe sequence end to end.
func TestHandleDepConnect_HappyPath(t *testing.T) {
	var srv *Server
	fake := &fakeDepConnectAgentConn{}
	fake.onInit = func(initData []byte) {
		var init messaging.HTTPTunnelStreamInit
		require.NoError(t, json.Unmarshal(initData, &init))
		assert.Equal(t, "tcp", init.Target)
		assert.Equal(t, "10.0.0.5:5432", init.DialAddr)
		go func() {
			srv.handleStreamChunk(&messaging.HTTPTunnelStreamChunk{RequestID: init.RequestID}) // dial-ack sentinel
			srv.handleStreamChunk(&messaging.HTTPTunnelStreamChunk{RequestID: init.RequestID, Data: []byte("pong")})
			srv.handleStreamChunk(&messaging.HTTPTunnelStreamChunk{RequestID: init.RequestID, IsClose: true})
		}()
	}
	srv = newDepConnectTestServer(t, fake, "ns1/cr1")

	httpSrv := httptest.NewServer(http.HandlerFunc(srv.handleDepConnect))
	defer httpSrv.Close()

	conn, err := depconnect.DialUpgrade(context.Background(),
		httpSrv.URL+"/api/depconnect/dataplane/p1/ns1/cr1?host=10.0.0.5&port=5432", nil, nil)
	require.NoError(t, err)
	defer conn.Close()

	buf := make([]byte, 4)
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(buf))

	// After the agent's IsClose, the server closes its side of the raw pipe.
	_, err = conn.Read(make([]byte, 1))
	assert.Error(t, err)
}
