// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package mcphandlers

import (
	"context"

	"github.com/openchoreo/openchoreo/internal/openchoreo-api/models"
)

// CRDListResponse represents the response for listing CRDs
type CRDListResponse struct {
	CRDs []*models.CRDInfo `json:"crds"`
}

func (h *MCPHandler) ListCRDs(ctx context.Context) (string, error) {
	crds, err := h.Services.SchemaService.ListCRDs(ctx)
	if err != nil {
		return "", err
	}

	response := CRDListResponse{CRDs: crds}
	return marshalResponse(response)
}

func (h *MCPHandler) GetCRD(ctx context.Context, crdName string) (string, error) {
	crd, err := h.Services.SchemaService.GetCRD(ctx, crdName)
	if err != nil {
		return "", err
	}

	return marshalResponse(crd)
}
