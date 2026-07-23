// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openchoreo/openchoreo/internal/depconnect"
)

// depConnectCapabilityHeader carries the capability minted by DepConnectHandler's
// resolve endpoint. It is opaque to occ — verified and consumed only here.
const depConnectCapabilityHeader = "X-Depconnect-Capability"

// DepConnectStreamHandler opens one dep-connect raw TCP tunnel per accepted local
// connection (forwarded by occ, one call per accepted connection — worklog §8): it
// verifies the capability minted by DepConnectHandler, dials the cluster-gateway's
// depconnect endpoint for the requested target, and bridges the two raw byte pipes.
// Like ExecHandler, this is registered outside the OpenAPI middleware chain since
// http.Hijacker is required and the strict response wrappers break it.
//
// URL: /depconnect/namespaces/{namespace}/components/{component}?key=...
// Header: X-Depconnect-Capability: <capability from :resolve>
type DepConnectStreamHandler struct {
	gatewayURL     string
	gatewayTLSConf *tls.Config
	verifyKey      ed25519.PublicKey
	logger         *slog.Logger
}

// NewDepConnectStreamHandler builds the stream handler. verifyKey must be the public
// half of the key DepConnectHandler signs capabilities with.
func NewDepConnectStreamHandler(gatewayURL string, gwTLSConf *tls.Config, verifyKey ed25519.PublicKey, logger *slog.Logger) *DepConnectStreamHandler {
	return &DepConnectStreamHandler{
		gatewayURL:     gatewayURL,
		gatewayTLSConf: gwTLSConf,
		verifyKey:      verifyKey,
		logger:         logger.With("component", "depconnect-stream-handler"),
	}
}

// ServeHTTP handles the raw TCP upgrade and bridges occ's connection to the
// dependency target through the cluster-gateway.
func (h *DepConnectStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !depconnect.IsUpgradeRequest(r) {
		http.Error(w, "expected a depconnect-tcp upgrade request", http.StatusBadRequest)
		return
	}

	// Parse URL: /depconnect/namespaces/{namespace}/components/{component}
	path := strings.TrimPrefix(r.URL.Path, "/depconnect/namespaces/")
	parts := strings.SplitN(path, "/components/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "invalid depconnect URL: expected /depconnect/namespaces/{ns}/components/{name}", http.StatusBadRequest)
		return
	}
	namespace := parts[0]
	component := parts[1]

	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "key query parameter is required", http.StatusBadRequest)
		return
	}

	capToken := r.Header.Get(depConnectCapabilityHeader)
	if capToken == "" {
		http.Error(w, "missing "+depConnectCapabilityHeader+" header", http.StatusUnauthorized)
		return
	}

	logger := h.logger.With("namespace", namespace, "component", component, "key", key)

	// Capability verification is the per-stream authorization check (worklog D2b):
	// done locally against the CP-signed capability, no extra round trip, and no
	// free-form destination ever accepted from occ (D9) — only a key already
	// present in the capability's target set can be dialed.
	claims, err := depconnect.VerifyCapability(capToken, h.verifyKey)
	if err != nil {
		logger.Warn("capability rejected", "error", err)
		http.Error(w, "invalid or expired capability", http.StatusUnauthorized)
		return
	}
	if claims.Namespace != namespace || claims.Component.Name != component {
		logger.Warn("capability does not match request", "capNamespace", claims.Namespace, "capComponent", claims.Component.Name)
		http.Error(w, "capability does not match request", http.StatusForbidden)
		return
	}
	target, ok := claims.TargetByKey(key)
	if !ok {
		http.Error(w, fmt.Sprintf("target %q not authorized by capability", key), http.StatusForbidden)
		return
	}

	gwConn, err := depconnect.DialUpgrade(r.Context(), h.buildGatewayURL(target), nil, h.gatewayTLSConf)
	if err != nil {
		var uerr *depconnect.UpgradeError
		if errors.As(err, &uerr) {
			logger.Warn("gateway rejected dep-connect dial", "status", uerr.StatusCode, "message", uerr.Message)
			http.Error(w, fmt.Sprintf("failed to connect to data plane: %s", uerr.Message), mapUpgradeStatus(uerr.StatusCode))
			return
		}
		logger.Error("Failed to connect to gateway depconnect endpoint", "error", err)
		http.Error(w, "failed to connect to data plane", http.StatusBadGateway)
		return
	}

	clientConn, err := depconnect.CompleteUpgrade(w)
	if err != nil {
		logger.Error("Failed to complete depconnect upgrade", "error", err)
		_ = gwConn.Close()
		return
	}

	logger.Info("Dep-connect stream established")
	depconnect.Pipe(clientConn, gwConn)
	logger.Info("Dep-connect stream ended")
}

// buildGatewayURL builds the cluster-gateway depconnect URL for the resolved target,
// mirroring ExecHandler.buildGatewayExecURL's routing convention.
func (h *DepConnectStreamHandler) buildGatewayURL(t depconnect.Target) string {
	u, _ := url.Parse(h.gatewayURL)
	u.Path = fmt.Sprintf("/api/depconnect/%s/%s/%s/%s", t.PlaneType, t.PlaneID, t.CRNamespace, t.CRName)
	q := u.Query()
	q.Set("host", t.Host)
	q.Set("port", strconv.Itoa(t.Port))
	u.RawQuery = q.Encode()
	return u.String()
}

// mapUpgradeStatus propagates gateway statuses that are meaningful to occ and
// collapses anything else to a generic 502.
func mapUpgradeStatus(status int) int {
	switch status {
	case http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusBadRequest:
		return status
	default:
		return http.StatusBadGateway
	}
}
