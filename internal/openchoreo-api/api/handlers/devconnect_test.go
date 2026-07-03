// Copyright 2025 The OpenChoreo Authors
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
	"github.com/openchoreo/openchoreo/internal/devconnect"
)

func devConnectScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := openchoreov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResolveEndpointAndResource(t *testing.T) {
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

	cl := fake.NewClientBuilder().WithScheme(devConnectScheme(t)).WithObjects(providerRB, providerRRB).Build()

	pub, priv, _ := ed25519.GenerateKey(nil)
	h := &DevConnectHandler{
		k8sClient:     cl,
		signer:        &capabilitySigner{privKey: priv, keyID: "k1", issuer: "cp", audience: devconnect.AgentAudience("dp-1"), ttl: 30 * time.Minute},
		agentEndpoint: "agent.dp-1:8443",
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	req := devconnect.ResolveRequest{
		Namespace:   "default",
		Project:     "doclet",
		Component:   "doclet-document",
		Environment: "development",
		Endpoints: []devconnect.EndpointDep{{
			Component: "backend-api", Name: "http", Visibility: "project",
			EnvBindings: devconnect.EndpointEnvBindings{Address: "BACKEND_API_URL"},
		}},
		Resources: []devconnect.ResourceDep{{
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

	// Capability verifies and carries the concrete (signed) dial targets.
	claims, err := devconnect.VerifyCapability(resp.Capability, pub, devconnect.AgentAudience("dp-1"))
	if err != nil {
		t.Fatalf("verify capability: %v", err)
	}
	if claims.Subject != "user:alice" || claims.Component.Name != "doclet-document" || claims.Env != "development" {
		t.Fatalf("unexpected capability claims: %+v", claims)
	}
	epT, ok := claims.TargetByKey("ep/backend-api/http")
	if !ok || epT.Host != "backend-api.dp-ns.svc.cluster.local" || epT.Port != 8080 {
		t.Fatalf("bad endpoint capability target: %+v ok=%v", epT, ok)
	}
	resT, ok := claims.TargetByKey("res/doclet-postgres")
	if !ok || resT.Host != "pg.dp-ns.svc.cluster.local" || resT.Port != 5432 {
		t.Fatalf("bad resource capability target: %+v ok=%v", resT, ok)
	}
}

func TestResolveUnreadyResourceIsUnconnectable(t *testing.T) {
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
	cl := fake.NewClientBuilder().WithScheme(devConnectScheme(t)).WithObjects(notReady).Build()
	_, priv, _ := ed25519.GenerateKey(nil)
	h := &DevConnectHandler{
		k8sClient: cl,
		signer:    &capabilitySigner{privKey: priv, audience: devconnect.AgentAudience("dp-1"), ttl: time.Minute},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	resp, err := h.resolve(context.Background(), devconnect.ResolveRequest{
		Namespace: "default", Project: "doclet", Component: "doclet-document", Environment: "development",
		Resources: []devconnect.ResourceDep{{Ref: "doclet-postgres", EnvBindings: map[string]string{"host": "DB_HOST", "port": "DB_PORT"}}},
	}, "user:alice")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resp.Targets) != 0 || len(resp.Unconnectable) != 1 || resp.Unconnectable[0].Ref != "doclet-postgres" {
		t.Fatalf("expected 1 unconnectable resource, got targets=%+v unconnectable=%+v", resp.Targets, resp.Unconnectable)
	}
}

func findTarget(t *testing.T, targets []devconnect.ResolvedTarget, key string) devconnect.ResolvedTarget {
	t.Helper()
	for _, tg := range targets {
		if tg.Key == key {
			return tg
		}
	}
	t.Fatalf("target %q not found in %+v", key, targets)
	return devconnect.ResolvedTarget{}
}
