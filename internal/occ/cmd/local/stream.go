// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/openchoreo/openchoreo/internal/depconnect"
	"github.com/openchoreo/openchoreo/internal/occ/auth"
	"github.com/openchoreo/openchoreo/internal/occ/cmd/config"
)

// dialDepConnectStream opens one raw TCP tunnel to the control plane's dep-connect
// stream endpoint for the given target key, presenting the capability minted by the
// resolve call. Called once per accepted local connection (worklog §8); the returned
// conn is a transparent byte pipe to the dialed dependency once the upgrade succeeds.
func dialDepConnectStream(ctx context.Context, namespace, component, key, capability string) (net.Conn, error) {
	cp, err := config.GetCurrentControlPlane()
	if err != nil {
		return nil, fmt.Errorf("failed to get control plane: %w", err)
	}

	streamURL, err := buildDepConnectStreamURL(cp.URL, namespace, component, key)
	if err != nil {
		return nil, fmt.Errorf("failed to build depconnect stream URL: %w", err)
	}

	header := http.Header{}
	if token := currentDepConnectToken(); token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	header.Set("X-Depconnect-Capability", capability)

	return depconnect.DialUpgrade(ctx, streamURL, header, nil)
}

// currentDepConnectToken mirrors the token-refresh pattern in
// internal/occ/cmd/component/exec.go.
func currentDepConnectToken() string {
	credential, err := config.GetCurrentCredential()
	if err != nil || credential == nil || credential.Token == "" {
		return ""
	}
	token := credential.Token
	if auth.IsTokenExpired(token) {
		if refreshed, rerr := auth.RefreshToken(); rerr == nil {
			token = refreshed
		}
	}
	return token
}

func buildDepConnectStreamURL(controlPlaneURL, namespace, component, key string) (string, error) {
	u, err := url.Parse(controlPlaneURL)
	if err != nil {
		return "", err
	}
	u.Path = fmt.Sprintf("/depconnect/namespaces/%s/components/%s", namespace, component)
	q := u.Query()
	q.Set("key", key)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
