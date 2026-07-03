// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devconnect

import (
	"crypto/ed25519"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// capabilitySigningMethod is EdDSA (Ed25519). Ed25519 keys are small and fast, and
// golang-jwt supports them out of the box.
var capabilitySigningMethod = jwt.SigningMethodEdDSA

// ComponentRef identifies the consuming component a capability is scoped to.
type ComponentRef struct {
	Project string `json:"project"`
	Name    string `json:"name"`
}

// Target is a single dialable destination the capability authorizes. The control
// plane resolves the concrete host:port and signs it here, so the agent dials only
// CP-authorized targets and never a free-form destination from the client (worklog
// D9). Key is the stable identifier the client references in StreamOpen.
type Target struct {
	Key   string `json:"key"`
	Proto string `json:"proto"` // "tcp" for v1
	Host  string `json:"host"`
	Port  int    `json:"port"`
}

// CapabilityClaims are the custom claims of the dev-connect capability JWT. The
// registered claims carry iss/sub/aud/exp/iat/jti; aud is scoped to a single plane
// (see AgentAudience) so a capability minted for one plane cannot be replayed
// against another.
type CapabilityClaims struct {
	jwt.RegisteredClaims
	Component ComponentRef `json:"component"`
	Env       string       `json:"env"`
	Targets   []Target     `json:"targets"`
}

// TargetByKey returns the authorized target with the given key, if present.
func (c *CapabilityClaims) TargetByKey(key string) (Target, bool) {
	for _, t := range c.Targets {
		if t.Key == key {
			return t, true
		}
	}
	return Target{}, false
}

// AgentAudience is the JWT audience value a capability must carry to be accepted by
// the dev-agent serving the given plane.
func AgentAudience(planeID string) string {
	return "dev-agent:" + planeID
}

// SignCapability mints a compact capability JWT signed with the control plane's
// Ed25519 private key. kid is set in the JWT header so the verifier can select the
// matching public key during rotation.
func SignCapability(claims *CapabilityClaims, priv ed25519.PrivateKey, kid string) (string, error) {
	tok := jwt.NewWithClaims(capabilitySigningMethod, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	return tok.SignedString(priv)
}

// VerifyCapability parses and validates a capability JWT against the control plane's
// Ed25519 public key, enforcing the signing method, a present-and-unexpired exp, and
// that audience is one of the token's aud values. On success it returns the claims.
func VerifyCapability(token string, pub ed25519.PublicKey, audience string) (*CapabilityClaims, error) {
	claims := &CapabilityClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{capabilitySigningMethod.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithAudience(audience),
	)
	if _, err := parser.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return pub, nil
	}); err != nil {
		return nil, fmt.Errorf("devconnect: verify capability: %w", err)
	}
	return claims, nil
}
