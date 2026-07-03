// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devconnect

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testClaims(planeID string, exp time.Time) *CapabilityClaims {
	return &CapabilityClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://cp.test",
			Subject:   "user:alice",
			Audience:  jwt.ClaimStrings{AgentAudience(planeID)},
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(exp.Add(-time.Hour)),
		},
		Component: ComponentRef{Project: "doclet", Name: "doclet-document"},
		Env:       "development",
		Targets: []Target{
			{Key: "res/doclet-postgres", Proto: "tcp", Host: "10.0.0.5", Port: 5432},
		},
	}
}

func TestCapabilityRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := SignCapability(testClaims("dp-1", time.Now().Add(30*time.Minute)), priv, "kid-1")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	got, err := VerifyCapability(token, pub, AgentAudience("dp-1"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Component.Name != "doclet-document" || got.Env != "development" {
		t.Fatalf("unexpected claims: %+v", got)
	}
	target, ok := got.TargetByKey("res/doclet-postgres")
	if !ok || target.Port != 5432 || target.Host != "10.0.0.5" {
		t.Fatalf("target lookup failed: %+v ok=%v", target, ok)
	}
	if _, ok := got.TargetByKey("nope"); ok {
		t.Fatal("expected missing target to be absent")
	}
}

func TestCapabilityWrongAudienceRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	token, _ := SignCapability(testClaims("dp-1", time.Now().Add(30*time.Minute)), priv, "kid-1")
	// Verify against a different plane's audience — must fail (no cross-plane replay).
	if _, err := VerifyCapability(token, pub, AgentAudience("dp-2")); err == nil {
		t.Fatal("expected verification against wrong audience to fail")
	}
}

func TestCapabilityExpiredRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	token, _ := SignCapability(testClaims("dp-1", time.Now().Add(-time.Minute)), priv, "kid-1")
	if _, err := VerifyCapability(token, pub, AgentAudience("dp-1")); err == nil {
		t.Fatal("expected expired capability to be rejected")
	}
}

func TestCapabilityWrongKeyRejected(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	token, _ := SignCapability(testClaims("dp-1", time.Now().Add(30*time.Minute)), priv, "kid-1")
	if _, err := VerifyCapability(token, otherPub, AgentAudience("dp-1")); err == nil {
		t.Fatal("expected verification with wrong key to fail")
	}
}
