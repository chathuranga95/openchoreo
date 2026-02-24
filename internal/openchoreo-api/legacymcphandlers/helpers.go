// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package legacymcphandlers

import (
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/legacyservices"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/handlerservices"
)

// LegacyMCPHandler is the original handler backed by legacy services.
// It is embedded in MCPHandler so unmigrated toolset methods are inherited.
type LegacyMCPHandler struct {
	Services *legacyservices.Services
}

// MCPHandler embeds LegacyMCPHandler and adds the new service layer.
// Migrated methods are defined directly on MCPHandler, shadowing the
// inherited legacy implementations.
type MCPHandler struct {
	LegacyMCPHandler
	services *handlerservices.Services
}

// NewMCPHandler creates an MCPHandler backed by both legacy and new services.
func NewMCPHandler(legacySvc *legacyservices.Services, svc *handlerservices.Services) *MCPHandler {
	return &MCPHandler{
		LegacyMCPHandler: LegacyMCPHandler{Services: legacySvc},
		services:         svc,
	}
}
