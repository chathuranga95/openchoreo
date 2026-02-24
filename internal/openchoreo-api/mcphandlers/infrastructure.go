// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package mcphandlers

import (
	"context"

	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

func (h *MCPHandler) ListComponentTypes(ctx context.Context, namespaceName string) (any, error) {
	result, err := h.services.ComponentTypeService.ListComponentTypes(ctx, namespaceName, services.ListOptions{})
	if err != nil {
		return nil, err
	}
	return wrapList("component_types", result.Items), nil
}

func (h *MCPHandler) GetComponentTypeSchema(ctx context.Context, namespaceName, ctName string) (any, error) {
	return h.services.ComponentTypeService.GetComponentTypeSchema(ctx, namespaceName, ctName)
}

func (h *MCPHandler) ListTraits(ctx context.Context, namespaceName string) (any, error) {
	result, err := h.services.TraitService.ListTraits(ctx, namespaceName, services.ListOptions{})
	if err != nil {
		return nil, err
	}
	return wrapList("traits", result.Items), nil
}

func (h *MCPHandler) GetTraitSchema(ctx context.Context, namespaceName, traitName string) (any, error) {
	return h.services.TraitService.GetTraitSchema(ctx, namespaceName, traitName)
}

func (h *MCPHandler) ListObservabilityPlanes(ctx context.Context, namespaceName string) (any, error) {
	result, err := h.services.ObservabilityPlaneService.ListObservabilityPlanes(ctx, namespaceName, services.ListOptions{})
	if err != nil {
		return nil, err
	}
	return wrapList("observability_planes", result.Items), nil
}
