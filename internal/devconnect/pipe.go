// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devconnect

import (
	"io"
	"net"
	"sync"
)

// Pipe copies bytes in both directions between a and b until either side ends,
// then closes both so the other copy unblocks. Used on both ends of the tunnel:
// the agent pipes stream⇄upstream, and occ pipes localConn⇄stream.
func Pipe(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		_ = a.Close()
		_ = b.Close()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		once.Do(closeBoth)
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
