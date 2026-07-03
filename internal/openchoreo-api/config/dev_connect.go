// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	coreconfig "github.com/openchoreo/openchoreo/internal/config"
)

// DevConnectConfig configures the `occ dev connect` resolve endpoint and the signed
// capability it issues (worklog §8.1–8.2). When disabled, the endpoint is not served.
type DevConnectConfig struct {
	// Enabled controls whether the dev-connect resolve endpoint is served.
	Enabled bool `koanf:"enabled"`
	// SigningKeyPath is the path to the PEM-encoded Ed25519 private key used to sign
	// capabilities. The dev-agent verifies with the matching public key.
	SigningKeyPath string `koanf:"signing_key_path"`
	// KeyID is set as the JWT `kid` header for key rotation.
	KeyID string `koanf:"key_id"`
	// Issuer is the capability JWT `iss` claim.
	Issuer string `koanf:"issuer"`
	// AgentEndpoint is the host:port occ dials to reach the dev-tunnel agent.
	AgentEndpoint string `koanf:"agent_endpoint"`
	// AgentCABundlePath is an optional PEM CA bundle occ uses to verify the agent's
	// TLS certificate. Empty means system roots.
	AgentCABundlePath string `koanf:"agent_ca_bundle_path"`
	// PlaneID scopes the capability audience (dev-agent:<plane-id>).
	PlaneID string `koanf:"plane_id"`
	// TTLSeconds is the capability lifetime in seconds.
	TTLSeconds int `koanf:"ttl_seconds"`
}

// DevConnectDefaults returns the default dev-connect configuration.
func DevConnectDefaults() DevConnectConfig {
	return DevConnectConfig{
		Enabled:    false,
		Issuer:     "openchoreo-control-plane",
		KeyID:      "dev-connect-1",
		TTLSeconds: 1800, // 30 minutes
	}
}

// Validate validates the dev-connect configuration.
func (c *DevConnectConfig) Validate(path *coreconfig.Path) coreconfig.ValidationErrors {
	var errs coreconfig.ValidationErrors
	if !c.Enabled {
		return errs
	}
	if c.SigningKeyPath == "" {
		errs = append(errs, coreconfig.Required(path.Child("signing_key_path")))
	}
	if c.AgentEndpoint == "" {
		errs = append(errs, coreconfig.Required(path.Child("agent_endpoint")))
	}
	if c.PlaneID == "" {
		errs = append(errs, coreconfig.Required(path.Child("plane_id")))
	}
	return errs
}
