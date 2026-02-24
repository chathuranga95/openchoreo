// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/openchoreo/openchoreo/internal/config"
	"github.com/openchoreo/openchoreo/pkg/mcp/legacytools"
	"github.com/openchoreo/openchoreo/pkg/mcp/tools"
)

// MCPConfig defines Model Context Protocol server settings.
type MCPConfig struct {
	// Enabled enables the MCP server endpoint.
	Enabled bool `koanf:"enabled"`
	// Toolsets is the list of enabled MCP toolsets.
	Toolsets []string `koanf:"toolsets"`
}

// MCPDefaults returns the default MCP configuration.
func MCPDefaults() MCPConfig {
	return MCPConfig{
		Enabled: true,
		Toolsets: []string{
			string(tools.ToolsetNamespace),
			string(tools.ToolsetProject),
			string(tools.ToolsetComponent),
			string(tools.ToolsetInfrastructure),
			string(legacytools.ToolsetBuild),
			string(legacytools.ToolsetDeployment),
			string(legacytools.ToolsetSchema),
			string(legacytools.ToolsetResource),
		},
	}
}

// validToolsets is the set of valid MCP toolset names.
var validToolsets = map[string]bool{
	string(tools.ToolsetNamespace):      true,
	string(tools.ToolsetProject):        true,
	string(tools.ToolsetComponent):      true,
	string(tools.ToolsetInfrastructure): true,
	// Legacy-only toolsets (used by legacy MCP handler, not the new OpenAPI one)
	string(legacytools.ToolsetBuild):      true,
	string(legacytools.ToolsetDeployment): true,
	string(legacytools.ToolsetSchema):     true,
	string(legacytools.ToolsetResource):   true,
}

// Validate validates the MCP configuration.
func (c *MCPConfig) Validate(path *config.Path) config.ValidationErrors {
	var errs config.ValidationErrors

	for i, ts := range c.Toolsets {
		if !validToolsets[ts] {
			errs = append(errs, config.Invalid(path.Child("toolsets").Index(i),
				fmt.Sprintf("unknown toolset %q; valid toolsets: namespace, project, component, build, deployment, infrastructure, schema, resource", ts)))
		}
	}

	return errs
}

// ParseToolsets converts the toolset strings to a map of ToolsetType for lookup.
func (c *MCPConfig) ParseToolsets() map[tools.ToolsetType]bool {
	result := make(map[tools.ToolsetType]bool, len(c.Toolsets))
	for _, ts := range c.Toolsets {
		result[tools.ToolsetType(ts)] = true
	}
	return result
}

// ParseLegacyToolsets converts the toolset strings to a map of legacy ToolsetType for lookup.
func (c *MCPConfig) ParseLegacyToolsets() map[legacytools.ToolsetType]bool {
	result := make(map[legacytools.ToolsetType]bool, len(c.Toolsets))
	for _, ts := range c.Toolsets {
		result[legacytools.ToolsetType(ts)] = true
	}
	return result
}
