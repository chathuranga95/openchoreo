// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusteragent

import (
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/openchoreo/openchoreo/internal/cluster-agent/messaging"
)

// l4Session represents an active L4 (raw TCP) tunnel stream in the agent.
// One session corresponds to a single TCP connection dialed to an in-cluster
// target on behalf of a client (e.g. `occ dev connect`). Unlike exec, an L4
// stream carries undifferentiated bytes in both directions, so chunk Data is
// the raw payload with no stream-type prefix.
type l4Session struct {
	requestID string
	conn      net.Conn
	queue     *byteQueue // gateway -> conn bytes, drained in order by the writer goroutine
	once      sync.Once
}

func (s *l4Session) close() {
	s.once.Do(func() {
		s.queue.close()
		_ = s.conn.Close()
	})
}

// byteQueue is an unbounded, order-preserving FIFO of byte chunks. It lets the
// agent's single message loop hand inbound chunks off without blocking (push is
// non-blocking) while a writer goroutine drains them to the TCP connection in
// arrival order. The unbounded backing slice is the per-stream flow-control gap
// tracked as risk #3; bounded credits are a follow-up.
type byteQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]byte
	closed bool
}

func newByteQueue() *byteQueue {
	q := &byteQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *byteQueue) push(b []byte) {
	q.mu.Lock()
	if !q.closed {
		q.chunks = append(q.chunks, b)
		q.cond.Signal()
	}
	q.mu.Unlock()
}

func (q *byteQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// pop blocks until a chunk is available or the queue is closed and drained.
// The boolean is false only when the queue is closed and empty.
func (q *byteQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.chunks) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.chunks) == 0 {
		return nil, false
	}
	b := q.chunks[0]
	q.chunks = q.chunks[1:]
	return b, true
}

// handleL4StreamInit opens a raw TCP connection to init.Path ("host:port") and
// bridges it to the gateway over the tunnel. Triggered when Target == "l4".
func (a *Agent) handleL4StreamInit(init *messaging.HTTPTunnelStreamInit) {
	logger := a.logger.With("requestID", init.RequestID, "target", init.Path)
	logger.Info("Received L4 stream init")

	conn, err := net.Dial("tcp", init.Path)
	if err != nil {
		// Surface the dial failure to the gateway as an immediate close; the
		// gateway treats the first chunk being a close as "dial failed".
		logger.Error("L4 dial failed", "error", err)
		a.sendStreamClose(init.RequestID, fmt.Sprintf("dial %s failed: %v", init.Path, err))
		return
	}

	session := &l4Session{
		requestID: init.RequestID,
		conn:      conn,
		queue:     newByteQueue(),
	}

	a.l4StreamsMu.Lock()
	a.l4Streams[init.RequestID] = session
	a.l4StreamsMu.Unlock()

	defer func() {
		session.close()
		a.l4StreamsMu.Lock()
		delete(a.l4Streams, init.RequestID)
		a.l4StreamsMu.Unlock()
		a.sendStreamClose(init.RequestID, "")
		logger.Info("L4 stream closed")
	}()

	// Readiness signal: an empty, non-close chunk tells the gateway the dial
	// succeeded and it may start forwarding client bytes. Registering the
	// session before this send guarantees inbound chunks find their session.
	a.sendStreamChunkRaw(init.RequestID, []byte{}, 0)

	// Inbound writer: gateway -> conn, in arrival order.
	go func() {
		for {
			b, ok := session.queue.pop()
			if !ok {
				return
			}
			if _, werr := conn.Write(b); werr != nil {
				session.close()
				return
			}
		}
	}()

	// Outbound: conn -> gateway. sendStreamChunk writes synchronously under the
	// agent's connection lock, so a slow tunnel naturally back-pressures the
	// TCP read here rather than buffering unboundedly.
	buf := make([]byte, 32*1024)
	for {
		n, rerr := conn.Read(buf)
		if n > 0 {
			chunk := &messaging.HTTPTunnelStreamChunk{
				RequestID: init.RequestID,
				Data:      append([]byte(nil), buf[:n]...),
			}
			if serr := a.sendStreamChunk(chunk); serr != nil {
				logger.Debug("failed to send L4 chunk", "error", serr)
				return
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				logger.Debug("L4 conn read ended", "error", rerr)
			}
			return
		}
	}
}

// routeL4Chunk delivers an inbound chunk to its L4 session and reports whether
// it matched one. It MUST stay non-blocking: it runs inline on the agent's
// single message loop, so blocking here would stall every tenant's tunnel.
func (a *Agent) routeL4Chunk(chunk *messaging.HTTPTunnelStreamChunk) bool {
	a.l4StreamsMu.Lock()
	session, ok := a.l4Streams[chunk.RequestID]
	a.l4StreamsMu.Unlock()
	if !ok {
		return false
	}

	if chunk.IsClose {
		session.close()
		return true
	}
	if len(chunk.Data) > 0 {
		// Data slices come from a fresh Unmarshal per message (not reused), so
		// they are safe to enqueue without copying — same assumption as exec.
		session.queue.push(chunk.Data)
	}
	return true
}
