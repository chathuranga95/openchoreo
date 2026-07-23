// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	coreconfig "github.com/openchoreo/openchoreo/internal/config"
)

// DepConnectConfig configures the `occ local` resolve endpoint and the signed
// capability it issues (worklog §8). The capability is minted and verified by
// openchoreo-api itself — there is no separate dev-tunnel agent — so this config
// only needs the signing key; the stream endpoint derives its verification key from
// it directly. When disabled, neither endpoint is served.
type DepConnectConfig struct {
	// Enabled controls whether the dep-connect resolve and stream endpoints are served.
	Enabled bool `koanf:"enabled"`
	// SigningKeyPath is the path to the PEM-encoded Ed25519 private key used to sign
	// and verify capabilities.
	SigningKeyPath string `koanf:"signing_key_path"`
	// KeyID is set as the JWT `kid` header for key rotation.
	KeyID string `koanf:"key_id"`
	// Issuer is the capability JWT `iss` claim.
	Issuer string `koanf:"issuer"`
	// TTLSeconds is the capability lifetime in seconds.
	TTLSeconds int `koanf:"ttl_seconds"`
}

// DepConnectDefaults returns the default dep-connect configuration.
func DepConnectDefaults() DepConnectConfig {
	return DepConnectConfig{
		Enabled:    false,
		Issuer:     "openchoreo-control-plane",
		KeyID:      "dep-connect-1",
		TTLSeconds: 1800, // 30 minutes
	}
}

// Validate validates the dep-connect configuration.
func (c *DepConnectConfig) Validate(path *coreconfig.Path) coreconfig.ValidationErrors {
	var errs coreconfig.ValidationErrors
	if !c.Enabled {
		return errs
	}
	if c.SigningKeyPath == "" {
		errs = append(errs, coreconfig.Required(path.Child("signing_key_path")))
	}
	return errs
}
