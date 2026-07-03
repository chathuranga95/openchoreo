// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devconnect

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := StreamOpen{Key: "ep/backend-api/http"}
	if err := WriteMessage(&buf, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out StreamOpen
	if err := ReadMessage(&buf, &out); err != nil {
		t.Fatalf("read: %v", err)
	}
	if out.Key != in.Key {
		t.Fatalf("round-trip mismatch: got %q want %q", out.Key, in.Key)
	}
}

func TestReadMessageRejectsOversizeLength(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], maxMessageSize+1)
	buf.Write(hdr[:])
	var out StreamOpen
	if err := ReadMessage(&buf, &out); err != ErrMessageTooLarge {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
}

func TestWriteMessageRejectsOversizeBody(t *testing.T) {
	var buf bytes.Buffer
	big := StreamOpen{Key: string(make([]byte, maxMessageSize+1))}
	if err := WriteMessage(&buf, big); err != ErrMessageTooLarge {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
}
