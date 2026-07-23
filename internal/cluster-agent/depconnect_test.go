// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusteragent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openchoreo/openchoreo/internal/cluster-agent/messaging"
)

// newDialTestAgent wires a fresh Agent + mockConnection together with the
// dialStreams map initialized, which newTestAgent leaves nil.
func newDialTestAgent(t *testing.T) (*Agent, *mockConnection) {
	t.Helper()
	agent := newTestAgent(t, "ws://unused", nil)
	mock := &mockConnection{}
	agent.conn = mock
	agent.dialStreams = make(map[string]*dialSession)
	return agent, mock
}

// startTestEchoListener starts a plain TCP listener that echoes back whatever it
// reads, standing in for the tunnelled dependency.
func startTestEchoListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, rerr := c.Read(buf)
					if n > 0 {
						if _, werr := c.Write(buf[:n]); werr != nil {
							return
						}
					}
					if rerr != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestAgent_HandleDialStreamInit_EchoRoundTrip(t *testing.T) {
	addr := startTestEchoListener(t)
	agent, mock := newDialTestAgent(t)

	done := make(chan struct{})
	go func() {
		agent.handleDialStreamInit(context.Background(), &messaging.HTTPTunnelStreamInit{
			RequestID: "req-1",
			Target:    "tcp",
			DialAddr:  addr,
		})
		close(done)
	}()

	// Dial-success sentinel chunk.
	require.Eventually(t, func() bool {
		return len(mock.getWrittenMessages()) >= 1
	}, 2*time.Second, 10*time.Millisecond)
	sentinel := decodeChunks(t, mock.getWrittenMessages()[:1])[0]
	assert.Equal(t, "req-1", sentinel.RequestID)
	assert.False(t, sentinel.IsClose)

	// A chunk relayed from the gateway is written to the dial target, which
	// echoes it back as an outbound chunk.
	require.True(t, agent.routeDialChunk(&messaging.HTTPTunnelStreamChunk{RequestID: "req-1", Data: []byte("ping")}))

	require.Eventually(t, func() bool {
		written := mock.getWrittenMessages()
		if len(written) < 2 {
			return false
		}
		for _, c := range decodeChunks(t, written[1:]) {
			if string(c.Data) == "ping" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)

	// Closing the session unblocks handleDialStreamInit.
	require.True(t, agent.routeDialChunk(&messaging.HTTPTunnelStreamChunk{RequestID: "req-1", IsClose: true}))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleDialStreamInit did not return after close")
	}
}

func TestAgent_HandleDialStreamInit_DialFailure(t *testing.T) {
	agent, mock := newDialTestAgent(t)

	agent.handleDialStreamInit(context.Background(), &messaging.HTTPTunnelStreamInit{
		RequestID: "req-2",
		Target:    "tcp",
		DialAddr:  "127.0.0.1:1", // nothing listens on port 1
	})

	written := decodeChunks(t, mock.getWrittenMessages())
	require.Len(t, written, 1)
	assert.Equal(t, "req-2", written[0].RequestID)
	assert.True(t, written[0].IsClose)
	assert.Contains(t, string(written[0].Data), "dial failed")
}

func TestAgent_HandleDialStreamInit_MissingDialAddr(t *testing.T) {
	agent, mock := newDialTestAgent(t)

	agent.handleDialStreamInit(context.Background(), &messaging.HTTPTunnelStreamInit{
		RequestID: "req-3",
		Target:    "tcp",
	})

	written := decodeChunks(t, mock.getWrittenMessages())
	require.Len(t, written, 1)
	assert.True(t, written[0].IsClose)
	assert.Contains(t, string(written[0].Data), "dialAddr is required")
}

func TestAgent_HandleDialStreamInit_DuplicateRequestIDRejected(t *testing.T) {
	addr := startTestEchoListener(t)
	agent, mock := newDialTestAgent(t)

	done := make(chan struct{})
	go func() {
		agent.handleDialStreamInit(context.Background(), &messaging.HTTPTunnelStreamInit{
			RequestID: "req-dup", Target: "tcp", DialAddr: addr,
		})
		close(done)
	}()
	require.Eventually(t, func() bool { return len(mock.getWrittenMessages()) >= 1 }, 2*time.Second, 10*time.Millisecond)

	// A second init with the same RequestID is rejected outright.
	agent.handleDialStreamInit(context.Background(), &messaging.HTTPTunnelStreamInit{
		RequestID: "req-dup", Target: "tcp", DialAddr: addr,
	})
	written := decodeChunks(t, mock.getWrittenMessages())
	require.Len(t, written, 2)
	assert.True(t, written[1].IsClose)
	assert.Contains(t, string(written[1].Data), "duplicate")

	require.True(t, agent.routeDialChunk(&messaging.HTTPTunnelStreamChunk{RequestID: "req-dup", IsClose: true}))
	<-done
}

func TestAgent_RouteDialChunk_UnknownRequestID(t *testing.T) {
	agent, _ := newDialTestAgent(t)
	assert.False(t, agent.routeDialChunk(&messaging.HTTPTunnelStreamChunk{RequestID: "nope", Data: []byte("x")}))
}
