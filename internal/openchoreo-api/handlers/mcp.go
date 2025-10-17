package handlers

import (
	"context"
	"encoding/json"
)

type APIMCPHandler struct {
	handler *Handler
}

func (h *APIMCPHandler) GetOrganization(name string) (string, error) {
	ctx := context.Background()
	if name == "" {
		return h.listOrganizations(ctx)
	} else {
		return h.getOrganizationByName(ctx, name)
	}
}

func (h *APIMCPHandler) listOrganizations(ctx context.Context) (string, error) {
	res, err := h.handler.services.OrganizationService.ListOrganizations(ctx)
	if err != nil {
		return "", err
	}
	// Return the JSON stringified result
	data, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (h *APIMCPHandler) getOrganizationByName(ctx context.Context, name string) (string, error) {
	res, err := h.handler.services.OrganizationService.GetOrganization(ctx, name)
	if err != nil {
		return "", err
	}
	// Return the JSON stringified result
	data, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
