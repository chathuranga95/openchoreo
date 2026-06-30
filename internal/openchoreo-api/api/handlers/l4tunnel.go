// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authz "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/controller"
	svcpkg "github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

// L4TunnelHandler proxies a raw-TCP (L4) tunnel WebSocket from a client (occ)
// to the cluster-gateway's /api/l4 endpoint. It authorizes the caller and
// resolves the dependency's in-cluster address server-side from the target
// component+endpoint+environment, so the client cannot tunnel to an arbitrary
// host:port. Foundation for `occ dev connect`.
type L4TunnelHandler struct {
	k8sClient      client.Client
	gatewayURL     string
	gatewayTLSConf *tls.Config
	authzChecker   *svcpkg.AuthzChecker
	logger         *slog.Logger
}

// NewL4TunnelHandler creates a new L4 tunnel handler.
func NewL4TunnelHandler(k8sClient client.Client, gatewayURL string, gwTLSConf *tls.Config, authzChecker *svcpkg.AuthzChecker, logger *slog.Logger) *L4TunnelHandler {
	return &L4TunnelHandler{
		k8sClient:      k8sClient,
		gatewayURL:     gatewayURL,
		gatewayTLSConf: gwTLSConf,
		authzChecker:   authzChecker,
		logger:         logger.With("component", "l4-tunnel-handler"),
	}
}

var l4Upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ServeHTTP handles the L4 tunnel WebSocket upgrade and bidirectional bridge.
// URL: /tunnel/namespaces/{namespace}/components/{component}?env=...&project=...&endpoint=...
func (h *L4TunnelHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	path := strings.TrimPrefix(r.URL.Path, "/tunnel/namespaces/")
	parts := strings.SplitN(path, "/components/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "invalid tunnel URL: expected /tunnel/namespaces/{ns}/components/{component}", http.StatusBadRequest)
		return
	}
	namespace := parts[0]
	component := parts[1]

	q := r.URL.Query()
	project := q.Get("project")
	envName := q.Get("env")
	endpoint := q.Get("endpoint")
	if envName == "" || endpoint == "" {
		http.Error(w, "env and endpoint query parameters are required", http.StatusBadRequest)
		return
	}

	logger := h.logger.With("namespace", namespace, "component", component, "env", envName, "endpoint", endpoint)
	logger.Info("L4 tunnel request received")

	// Authorize: caller must be able to view the target component in this env.
	if h.authzChecker == nil {
		http.Error(w, "authorization not configured", http.StatusInternalServerError)
		return
	}
	if err := h.authzChecker.Check(ctx, svcpkg.CheckRequest{
		Action:       authz.ActionViewComponent,
		ResourceType: "component",
		ResourceID:   component,
		Hierarchy: authz.ResourceHierarchy{
			Namespace: namespace,
			Project:   project,
		},
		Context: authz.Context{
			Resource: authz.ResourceAttribute{
				Environment: svcpkg.FormatDualScopedResourceName(namespace, envName, false),
			},
		},
	}); err != nil {
		if errors.Is(err, svcpkg.ErrForbidden) {
			http.Error(w, "you do not have permission to tunnel to this component", http.StatusForbidden)
			return
		}
		logger.Error("Authorization check failed", "error", err)
		http.Error(w, "authorization check failed", http.StatusInternalServerError)
		return
	}

	// Resolve the dependency's in-cluster address server-side. The client only
	// names the target component+endpoint; it cannot pick an arbitrary host:port.
	ep, reason := lookupProviderEndpoint(ctx, h.k8sClient, namespace, project, component, endpoint, envName)
	if ep == nil {
		http.Error(w, "dependency not resolvable: "+reason, http.StatusBadRequest)
		return
	}
	target := fmt.Sprintf("%s:%d", ep.ServiceURL.Host, ep.ServiceURL.Port)

	// Resolve the environment to gateway plane coordinates.
	plane, err := h.resolvePlane(ctx, namespace, envName)
	if err != nil {
		logger.Warn("Failed to resolve data plane", "error", err)
		http.Error(w, fmt.Sprintf("failed to resolve data plane: %v", err), http.StatusServiceUnavailable)
		return
	}

	// Upgrade the client connection to WebSocket.
	clientConn, err := l4Upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Failed to upgrade to WebSocket", "error", err)
		return
	}
	defer clientConn.Close()

	gwURL, err := h.buildGatewayL4URL(plane, target)
	if err != nil {
		logger.Error("Failed to build gateway L4 URL", "error", err)
		closeClientWS(clientConn, websocket.CloseInternalServerErr, "internal error")
		return
	}

	gwDialer := websocket.Dialer{TLSClientConfig: h.gatewayTLSConf}
	gwConn, _, err := gwDialer.DialContext(ctx, gwURL, nil)
	if err != nil {
		logger.Error("Failed to connect to gateway L4 endpoint", "error", err)
		closeClientWS(clientConn, websocket.CloseInternalServerErr, "failed to connect to data plane")
		return
	}
	defer gwConn.Close()

	logger.Info("L4 tunnel established", "target", target, "plane", plane.planeID)

	// Bidirectional bridge: client ↔ gateway (raw bytes, binary frames).
	done := make(chan struct{}, 2)

	// client → gateway
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			mt, msg, err := clientConn.ReadMessage()
			if err != nil {
				return
			}
			if err := gwConn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}()

	// gateway → client
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			mt, msg, err := gwConn.ReadMessage()
			if err != nil {
				closeCode := websocket.CloseNormalClosure
				closeText := ""
				var ce *websocket.CloseError
				if errors.As(err, &ce) {
					closeCode = ce.Code
					closeText = ce.Text
				}
				closeClientWS(clientConn, closeCode, closeText)
				return
			}
			if err := clientConn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}()

	<-done
	logger.Info("L4 tunnel ended", "target", target)
}

// resolvePlane resolves an environment to its gateway plane coordinates,
// reusing the same plane derivation as exec.
func (h *L4TunnelHandler) resolvePlane(ctx context.Context, namespace, envName string) (execPlaneInfo, error) {
	env := &openchoreov1alpha1.Environment{}
	if err := h.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: envName}, env); err != nil {
		return execPlaneInfo{}, fmt.Errorf("environment %q not found in namespace %q: %w", envName, namespace, err)
	}
	if env.Spec.DataPlaneRef == nil {
		return execPlaneInfo{}, fmt.Errorf("environment %q has no data plane reference", envName)
	}
	dpResult, err := controller.GetDataPlaneFromRef(ctx, h.k8sClient, env.Namespace, env.Spec.DataPlaneRef)
	if err != nil {
		return execPlaneInfo{}, err
	}
	plane := resolveExecPlaneInfo(dpResult)
	if plane.planeID == "" {
		return execPlaneInfo{}, fmt.Errorf("failed to determine plane ID for environment %q", envName)
	}
	return plane, nil
}

// buildGatewayL4URL builds the gateway L4 WebSocket URL for a plane and target.
func (h *L4TunnelHandler) buildGatewayL4URL(plane execPlaneInfo, target string) (string, error) {
	u, err := url.Parse(h.gatewayURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = fmt.Sprintf("/api/l4/%s/%s/%s/%s",
		plane.planeType, plane.planeID, plane.crNamespace, plane.crName)
	q := u.Query()
	q.Set("target", target)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// closeClientWS sends a WebSocket close frame to the client. Unlike exec's
// writeWSError it injects no data byte, since the L4 stream is raw bytes.
func closeClientWS(conn *websocket.Conn, code int, text string) {
	_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, text))
}
