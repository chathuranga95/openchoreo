// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openchoreo/openchoreo/internal/depconnect"
)

func testStreamHandler(t *testing.T, gatewayURL string, pub ed25519.PublicKey) *DepConnectStreamHandler {
	t.Helper()
	return NewDepConnectStreamHandler(gatewayURL, nil, pub, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func signTestCapability(t *testing.T, priv ed25519.PrivateKey, namespace, component, env string, targets []depconnect.Target) string {
	t.Helper()
	tok, err := depconnect.SignCapability(&depconnect.CapabilityClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user:test",
			Audience:  jwt.ClaimStrings{depconnect.CapabilityAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
		},
		Namespace: namespace,
		Component: depconnect.ComponentRef{Project: "doclet", Name: component},
		Env:       env,
		Targets:   targets,
	}, priv, "k1")
	require.NoError(t, err)
	return tok
}

func newStreamRequest(target, capability string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Upgrade", depconnect.UpgradeProtocol)
	if capability != "" {
		req.Header.Set(depConnectCapabilityHeader, capability)
	}
	return req
}

func TestDepConnectStream_NotAnUpgradeRequest(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	h := testStreamHandler(t, "http://gateway.invalid", priv.Public().(ed25519.PublicKey))
	req := httptest.NewRequest(http.MethodGet, "/depconnect/namespaces/default/components/doclet-document?key=x", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDepConnectStream_InvalidURL(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	h := testStreamHandler(t, "http://gateway.invalid", priv.Public().(ed25519.PublicKey))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, newStreamRequest("/depconnect/namespaces/default?key=x", "cap"))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDepConnectStream_MissingKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	h := testStreamHandler(t, "http://gateway.invalid", priv.Public().(ed25519.PublicKey))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, newStreamRequest("/depconnect/namespaces/default/components/doclet-document", "cap"))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "key query parameter")
}

func TestDepConnectStream_MissingCapability(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	h := testStreamHandler(t, "http://gateway.invalid", priv.Public().(ed25519.PublicKey))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, newStreamRequest("/depconnect/namespaces/default/components/doclet-document?key=x", ""))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestDepConnectStream_InvalidCapability(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	h := testStreamHandler(t, "http://gateway.invalid", priv.Public().(ed25519.PublicKey))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, newStreamRequest("/depconnect/namespaces/default/components/doclet-document?key=x", "not-a-jwt"))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestDepConnectStream_CapabilityComponentMismatch(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	h := testStreamHandler(t, "http://gateway.invalid", priv.Public().(ed25519.PublicKey))
	// Signed correctly, but for a different component than the URL names.
	capToken := signTestCapability(t, priv, "default", "other-component", "development", []depconnect.Target{
		{Key: "res/x", Host: "10.0.0.1", Port: 1234},
	})
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, newStreamRequest("/depconnect/namespaces/default/components/doclet-document?key=res/x", capToken))

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDepConnectStream_TargetNotInCapability(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	h := testStreamHandler(t, "http://gateway.invalid", priv.Public().(ed25519.PublicKey))
	capToken := signTestCapability(t, priv, "default", "doclet-document", "development", []depconnect.Target{
		{Key: "res/other", Host: "10.0.0.1", Port: 1234},
	})
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, newStreamRequest("/depconnect/namespaces/default/components/doclet-document?key=res/x", capToken))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "not authorized")
}

// TestDepConnectStream_HappyPath drives the handler against a fake gateway that
// itself speaks the depconnect-tcp upgrade protocol, verifying the full
// verify-capability -> dial-gateway -> bridge sequence end to end.
func TestDepConnectStream_HappyPath(t *testing.T) {
	fakeGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/depconnect/dataplane/dp-1/default/default", r.URL.Path)
		assert.Equal(t, "10.0.0.5", r.URL.Query().Get("host"))
		assert.Equal(t, "5432", r.URL.Query().Get("port"))

		conn, err := depconnect.CompleteUpgrade(w)
		require.NoError(t, err)
		defer conn.Close()
		buf := make([]byte, 5)
		_, err = io.ReadFull(conn, buf)
		require.NoError(t, err)
		require.Equal(t, "hello", string(buf))
		_, _ = conn.Write([]byte("world"))
	}))
	defer fakeGateway.Close()

	_, priv, _ := ed25519.GenerateKey(nil)
	h := testStreamHandler(t, fakeGateway.URL, priv.Public().(ed25519.PublicKey))
	capToken := signTestCapability(t, priv, "default", "doclet-document", "development", []depconnect.Target{
		{
			Key: "res/doclet-postgres", Proto: "tcp", Host: "10.0.0.5", Port: 5432,
			PlaneType: "dataplane", PlaneID: "dp-1", CRNamespace: "default", CRName: "default",
		},
	})

	streamSrv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer streamSrv.Close()

	header := http.Header{}
	header.Set(depConnectCapabilityHeader, capToken)
	conn, err := depconnect.DialUpgrade(context.Background(),
		streamSrv.URL+"/depconnect/namespaces/default/components/doclet-document?key=res/doclet-postgres", header, nil)
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte("hello"))
	require.NoError(t, err)
	buf := make([]byte, 5)
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "world", string(buf))
}
