// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package depconnect

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpgradeRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsUpgradeRequest(r) {
			http.Error(w, "not an upgrade", http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-Test") != "hello" {
			http.Error(w, "missing header", http.StatusBadRequest)
			return
		}
		conn, err := CompleteUpgrade(w)
		if err != nil {
			t.Errorf("server CompleteUpgrade: %v", err)
			return
		}
		defer conn.Close()

		buf := make([]byte, 5)
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if string(buf) != "hello" {
			t.Errorf("server got %q, want hello", buf)
		}
		if _, err := conn.Write([]byte("world")); err != nil {
			t.Errorf("server write: %v", err)
		}
	}))
	defer srv.Close()

	header := http.Header{}
	header.Set("X-Test", "hello")
	conn, err := DialUpgrade(context.Background(), srv.URL, header, nil)
	if err != nil {
		t.Fatalf("DialUpgrade: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf) != "world" {
		t.Fatalf("client got %q, want world", buf)
	}
}

func TestDialUpgradeNonSwitchingProtocolsIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := DialUpgrade(context.Background(), srv.URL, http.Header{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var uerr *UpgradeError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *UpgradeError, got %T: %v", err, err)
	}
	if uerr.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", uerr.StatusCode, http.StatusForbidden)
	}
}

func TestIsUpgradeRequest(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	if IsUpgradeRequest(req) {
		t.Fatal("expected no Upgrade header to not match")
	}
	req.Header.Set("Upgrade", UpgradeProtocol)
	if !IsUpgradeRequest(req) {
		t.Fatal("expected matching Upgrade header to match")
	}
	req.Header.Set("Upgrade", "websocket")
	if IsUpgradeRequest(req) {
		t.Fatal("expected a different Upgrade value to not match")
	}
}
