// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/depconnect"
)

func depConnectScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := openchoreov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// testEnvironmentAndDataPlane builds an Environment (in ns "default", named
// "development") referencing a DataPlane with PlaneID "dp-1" — every dep-connect
// dependency resolves through this same plane (resolveDepConnectPlane runs once per
// request).
func testEnvironmentAndDataPlane() (*openchoreov1alpha1.Environment, *openchoreov1alpha1.DataPlane) {
	dp := &openchoreov1alpha1.DataPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"},
		Spec:       openchoreov1alpha1.DataPlaneSpec{PlaneID: "dp-1"},
	}
	env := &openchoreov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "development", Namespace: "default"},
		Spec: openchoreov1alpha1.EnvironmentSpec{
			DataPlaneRef: &openchoreov1alpha1.DataPlaneRef{Kind: openchoreov1alpha1.DataPlaneRefKindDataPlane, Name: "default"},
		},
	}
	return env, dp
}

func TestResolveEndpointAndResource(t *testing.T) {
	env, dp := testEnvironmentAndDataPlane()
	providerRB := &openchoreov1alpha1.ReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-api-development", Namespace: "default"},
		Spec: openchoreov1alpha1.ReleaseBindingSpec{
			Owner:       openchoreov1alpha1.ReleaseBindingOwner{ProjectName: "doclet", ComponentName: "backend-api"},
			Environment: "development",
		},
		Status: openchoreov1alpha1.ReleaseBindingStatus{
			Endpoints: []openchoreov1alpha1.EndpointURLStatus{{
				Name: "http",
				ServiceURL: &openchoreov1alpha1.EndpointURL{
					Scheme: "http", Host: "backend-api.dp-ns.svc.cluster.local", Port: 8080, Path: "/api",
				},
			}},
		},
	}
	providerRRB := &openchoreov1alpha1.ResourceReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "doclet-postgres-development", Namespace: "default", Generation: 1},
		Spec: openchoreov1alpha1.ResourceReleaseBindingSpec{
			Owner:       openchoreov1alpha1.ResourceReleaseBindingOwner{ProjectName: "doclet", ResourceName: "doclet-postgres"},
			Environment: "development",
		},
		Status: openchoreov1alpha1.ResourceReleaseBindingStatus{
			Conditions: []metav1.Condition{{
				Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 1,
				Reason: "Ready", LastTransitionTime: metav1.Now(),
			}},
			Outputs: []openchoreov1alpha1.ResolvedResourceOutput{
				{Name: "host", Value: "pg.dp-ns.svc.cluster.local"},
				{Name: "port", Value: "5432"},
				{Name: "database", Value: "doclet"},
				{Name: "password", SecretKeyRef: &openchoreov1alpha1.SecretKeyRef{Name: "pg-secret", Key: "password"}},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(depConnectScheme(t)).WithObjects(env, dp, providerRB, providerRRB).Build()

	_, priv, _ := ed25519.GenerateKey(nil)
	h := &DepConnectHandler{
		k8sClient: cl,
		signer:    &capabilitySigner{privKey: priv, keyID: "k1", issuer: "cp", ttl: 30 * time.Minute},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	req := depconnect.ResolveRequest{
		Namespace:   "default",
		Project:     "doclet",
		Component:   "doclet-document",
		Environment: "development",
		Endpoints: []depconnect.EndpointDep{{
			Component: "backend-api", Name: "http", Visibility: "project",
			EnvBindings: depconnect.EndpointEnvBindings{Address: "BACKEND_API_URL"},
		}},
		Resources: []depconnect.ResourceDep{{
			Ref: "doclet-postgres",
			EnvBindings: map[string]string{
				"host": "DB_HOST", "port": "DB_PORT", "database": "DB_NAME", "password": "DB_PASSWORD",
			},
		}},
	}

	resp, err := h.resolve(context.Background(), req, "user:alice")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resp.Unconnectable) != 0 {
		t.Fatalf("unexpected unconnectable: %+v", resp.Unconnectable)
	}
	if len(resp.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d: %+v", len(resp.Targets), resp.Targets)
	}

	// Endpoint render info.
	ep := findTarget(t, resp.Targets, "ep/backend-api/http")
	if ep.Endpoint == nil || ep.Endpoint.Scheme != "http" || ep.Endpoint.BasePath != "/api" || ep.Endpoint.Bindings.Address != "BACKEND_API_URL" {
		t.Fatalf("bad endpoint render: %+v", ep.Endpoint)
	}
	// Resource render info: value-kind statics present, secret omitted.
	res := findTarget(t, resp.Targets, "res/doclet-postgres")
	if res.Resource == nil || res.Resource.HostEnv != "DB_HOST" || res.Resource.PortEnv != "DB_PORT" {
		t.Fatalf("bad resource render: %+v", res.Resource)
	}
	if res.Resource.StaticEnv["DB_NAME"] != "doclet" {
		t.Fatalf("expected DB_NAME=doclet, got %+v", res.Resource.StaticEnv)
	}
	if len(res.Resource.OmittedSecretEnv) != 1 || res.Resource.OmittedSecretEnv[0] != "DB_PASSWORD" {
		t.Fatalf("expected DB_PASSWORD omitted (secret), got %+v", res.Resource.OmittedSecretEnv)
	}

	// Capability verifies and carries the concrete (signed) dial + plane-routing targets.
	pub := priv.Public().(ed25519.PublicKey)
	claims, err := depconnect.VerifyCapability(resp.Capability, pub)
	if err != nil {
		t.Fatalf("verify capability: %v", err)
	}
	if claims.Subject != "user:alice" || claims.Namespace != "default" || claims.Component.Name != "doclet-document" || claims.Env != "development" {
		t.Fatalf("unexpected capability claims: %+v", claims)
	}
	epT, ok := claims.TargetByKey("ep/backend-api/http")
	if !ok || epT.Host != "backend-api.dp-ns.svc.cluster.local" || epT.Port != 8080 {
		t.Fatalf("bad endpoint capability target: %+v ok=%v", epT, ok)
	}
	if epT.PlaneType != "dataplane" || epT.PlaneID != "dp-1" || epT.CRNamespace != "default" || epT.CRName != "default" {
		t.Fatalf("bad endpoint capability plane routing: %+v", epT)
	}
	resT, ok := claims.TargetByKey("res/doclet-postgres")
	if !ok || resT.Host != "pg.dp-ns.svc.cluster.local" || resT.Port != 5432 {
		t.Fatalf("bad resource capability target: %+v ok=%v", resT, ok)
	}
	if resT.PlaneType != "dataplane" || resT.PlaneID != "dp-1" {
		t.Fatalf("bad resource capability plane routing: %+v", resT)
	}
}

func TestResolveUnreadyResourceIsUnconnectable(t *testing.T) {
	env, dp := testEnvironmentAndDataPlane()
	// ResourceReleaseBinding present but not Ready → reported unconnectable, not an error.
	notReady := &openchoreov1alpha1.ResourceReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "doclet-postgres-development", Namespace: "default", Generation: 1},
		Spec: openchoreov1alpha1.ResourceReleaseBindingSpec{
			Owner:       openchoreov1alpha1.ResourceReleaseBindingOwner{ProjectName: "doclet", ResourceName: "doclet-postgres"},
			Environment: "development",
		},
		Status: openchoreov1alpha1.ResourceReleaseBindingStatus{
			Conditions: []metav1.Condition{{
				Type: "Ready", Status: metav1.ConditionFalse, ObservedGeneration: 1,
				Reason: "Provisioning", LastTransitionTime: metav1.Now(),
			}},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(depConnectScheme(t)).WithObjects(env, dp, notReady).Build()
	_, priv, _ := ed25519.GenerateKey(nil)
	h := &DepConnectHandler{
		k8sClient: cl,
		signer:    &capabilitySigner{privKey: priv, ttl: time.Minute},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	resp, err := h.resolve(context.Background(), depconnect.ResolveRequest{
		Namespace: "default", Project: "doclet", Component: "doclet-document", Environment: "development",
		Resources: []depconnect.ResourceDep{{Ref: "doclet-postgres", EnvBindings: map[string]string{"host": "DB_HOST", "port": "DB_PORT"}}},
	}, "user:alice")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resp.Targets) != 0 || len(resp.Unconnectable) != 1 || resp.Unconnectable[0].Ref != "doclet-postgres" {
		t.Fatalf("expected 1 unconnectable resource, got targets=%+v unconnectable=%+v", resp.Targets, resp.Unconnectable)
	}
}

func TestResolveMissingDataPlaneRefFails(t *testing.T) {
	// Environment with no DataPlaneRef: resolution fails outright rather than
	// silently marking every dependency unconnectable.
	env := &openchoreov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "development", Namespace: "default"},
	}
	cl := fake.NewClientBuilder().WithScheme(depConnectScheme(t)).WithObjects(env).Build()
	_, priv, _ := ed25519.GenerateKey(nil)
	h := &DepConnectHandler{
		k8sClient: cl,
		signer:    &capabilitySigner{privKey: priv, ttl: time.Minute},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	_, err := h.resolve(context.Background(), depconnect.ResolveRequest{
		Namespace: "default", Project: "doclet", Component: "doclet-document", Environment: "development",
	}, "user:alice")
	if err == nil {
		t.Fatal("expected an error when the environment has no data plane reference")
	}
}

func findTarget(t *testing.T, targets []depconnect.ResolvedTarget, key string) depconnect.ResolvedTarget {
	t.Helper()
	for _, tg := range targets {
		if tg.Key == key {
			return tg
		}
	}
	t.Fatalf("target %q not found in %+v", key, targets)
	return depconnect.ResolvedTarget{}
}
