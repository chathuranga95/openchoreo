// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package mcphandlers

import (
	"context"
)

func (h *MCPHandler) ApplyResource(ctx context.Context, json string) (string, error) {
	result, err := h.Services.ResourceService.ApplyResourceFromJSON(ctx, json)
	if err != nil {
		return "", err
	}

	return marshalResponse(result)
}

func (h *MCPHandler) GetResource(ctx context.Context, kind, name, namespace string) (string, error) {
	result, err := h.Services.ResourceService.GetResourceFromKind(ctx, kind, name, namespace)
	if err != nil {
		return "", err
	}

	return marshalResponse(result)
}

func (h *MCPHandler) DeleteResource(ctx context.Context, kind, name, namespace string) (string, error) {
	result, err := h.Services.ResourceService.DeleteResourceFromKind(ctx, kind, name, namespace)
	if err != nil {
		return "", err
	}

	return marshalResponse(result)
}

func (h *MCPHandler) ListResources(ctx context.Context, kind, namespace string) (string, error) {
	result, err := h.Services.ResourceService.ListResourcesFromKind(ctx, kind, namespace)
	if err != nil {
		return "", err
	}

	return marshalResponse(result)
}
