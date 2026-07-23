// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package depconnect

import (
	"io"
	"sync"
)

// Pipe copies bytes in both directions between a and b until either side ends,
// then closes both so the other copy unblocks. a and b need only be byte streams
// (net.Conn, a WebSocket-backed adapter, ...), not necessarily the same kind.
func Pipe(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() {
		_ = a.Close()
		_ = b.Close()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src io.ReadWriteCloser) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		once.Do(closeBoth)
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
