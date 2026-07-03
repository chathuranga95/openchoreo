// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authz "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/devconnect"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/config"
	svcpkg "github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	authmw "github.com/openchoreo/openchoreo/internal/server/middleware/auth"
)

// DevConnectHandler serves POST /api/v1/dev/connect:resolve — it resolves a workload's
// declared dependencies against provider status and returns connection targets plus a
// short-lived, CP-signed capability (worklog §8.1–8.2). v1 = connectivity only: it emits
// non-secret env and skips secret-backed outputs (phase 2).
type DevConnectHandler struct {
	k8sClient     client.Client
	authzChecker  *svcpkg.AuthzChecker
	signer        *capabilitySigner
	agentEndpoint string
	agentCABundle string
	logger        *slog.Logger
}

// NewDevConnectHandler loads the signing key and builds the handler.
func NewDevConnectHandler(k8sClient client.Client, authzChecker *svcpkg.AuthzChecker, cfg config.DevConnectConfig, logger *slog.Logger) (*DevConnectHandler, error) {
	priv, err := loadEd25519PrivateKeyPEM(cfg.SigningKeyPath)
	if err != nil {
		return nil, fmt.Errorf("dev-connect: load signing key: %w", err)
	}
	var caBundle string
	if cfg.AgentCABundlePath != "" {
		b, rerr := os.ReadFile(cfg.AgentCABundlePath)
		if rerr != nil {
			return nil, fmt.Errorf("dev-connect: read agent CA bundle: %w", rerr)
		}
		caBundle = string(b)
	}
	return &DevConnectHandler{
		k8sClient:    k8sClient,
		authzChecker: authzChecker,
		signer: &capabilitySigner{
			privKey:  priv,
			keyID:    cfg.KeyID,
			issuer:   cfg.Issuer,
			audience: devconnect.AgentAudience(cfg.PlaneID),
			ttl:      time.Duration(cfg.TTLSeconds) * time.Second,
		},
		agentEndpoint: cfg.AgentEndpoint,
		agentCABundle: caBundle,
		logger:        logger.With("component", "dev-connect-handler"),
	}, nil
}

// ServeHTTP handles the resolve request.
func (h *DevConnectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req devconnect.ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Namespace == "" || req.Project == "" || req.Component == "" || req.Environment == "" {
		http.Error(w, "namespace, project, component and environment are required", http.StatusBadRequest)
		return
	}

	if h.authzChecker != nil {
		if err := h.authzChecker.Check(ctx, svcpkg.CheckRequest{
			Action:       authz.ActionConnectComponent,
			ResourceType: "component",
			ResourceID:   req.Component,
			Hierarchy: authz.ResourceHierarchy{
				Namespace: req.Namespace,
				Project:   req.Project,
			},
			Context: authz.Context{
				Resource: authz.ResourceAttribute{
					Environment: svcpkg.FormatDualScopedResourceName(req.Namespace, req.Environment, false),
				},
			},
		}); err != nil {
			if errors.Is(err, svcpkg.ErrForbidden) {
				http.Error(w, "you do not have permission to connect to this component", http.StatusForbidden)
				return
			}
			h.logger.Error("authorization check failed", "error", err)
			http.Error(w, "authorization check failed", http.StatusInternalServerError)
			return
		}
	}

	subject := "unknown"
	if sc, ok := authmw.GetSubjectContext(r); ok && sc != nil {
		subject = sc.ID
	}

	resp, err := h.resolve(ctx, req, subject)
	if err != nil {
		h.logger.Error("dependency resolution failed", "error", err)
		http.Error(w, "dependency resolution failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Error("failed to encode response", "error", err)
	}
}

// resolve turns declared dependencies into connection targets + a signed capability.
func (h *DevConnectHandler) resolve(ctx context.Context, req devconnect.ResolveRequest, subject string) (*devconnect.ResolveResponse, error) {
	var (
		capTargets   []devconnect.Target
		respTargets  []devconnect.ResolvedTarget
		unconnutable []devconnect.Unconnectable
	)

	for _, dep := range req.Endpoints {
		key := "ep/" + dep.Component + "/" + dep.Name
		providerProject := dep.Project
		if providerProject == "" {
			providerProject = req.Project
		}
		url, err := h.resolveEndpoint(ctx, req.Namespace, providerProject, dep.Component, dep.Name, dep.Visibility, req.Environment)
		if err != nil {
			unconnutable = append(unconnutable, devconnect.Unconnectable{Ref: key, Reason: err.Error()})
			continue
		}
		capTargets = append(capTargets, devconnect.Target{Key: key, Proto: "tcp", Host: url.Host, Port: int(url.Port)})
		respTargets = append(respTargets, devconnect.ResolvedTarget{
			Key:      key,
			Proto:    "tcp",
			Endpoint: &devconnect.EndpointRender{Scheme: url.Scheme, BasePath: url.Path, Bindings: dep.EnvBindings},
		})
	}

	for _, dep := range req.Resources {
		render, target, err := h.resolveResource(ctx, req.Namespace, req.Project, dep, req.Environment)
		if err != nil {
			unconnutable = append(unconnutable, devconnect.Unconnectable{Ref: dep.Ref, Reason: err.Error()})
			continue
		}
		capTargets = append(capTargets, target)
		respTargets = append(respTargets, devconnect.ResolvedTarget{Key: target.Key, Proto: "tcp", Resource: render})
	}

	capability, err := h.signer.sign(subject, devconnect.ComponentRef{Project: req.Project, Name: req.Component}, req.Environment, capTargets)
	if err != nil {
		return nil, fmt.Errorf("sign capability: %w", err)
	}

	return &devconnect.ResolveResponse{
		Agent:         devconnect.AgentEndpoint{Endpoint: h.agentEndpoint, CABundle: h.agentCABundle},
		Capability:    capability,
		Targets:       respTargets,
		Unconnectable: unconnutable,
	}, nil
}

// resolveEndpoint finds the provider ReleaseBinding and reads the named endpoint's
// in-cluster ServiceURL (for project/namespace visibility).
func (h *DevConnectHandler) resolveEndpoint(ctx context.Context, ns, project, component, epName, visibility, env string) (*openchoreov1alpha1.EndpointURL, error) {
	rb, err := h.findReleaseBinding(ctx, ns, project, component, env)
	if err != nil {
		return nil, err
	}
	for i := range rb.Status.Endpoints {
		ep := rb.Status.Endpoints[i]
		if ep.Name != epName {
			continue
		}
		url := urlForVisibility(ep, openchoreov1alpha1.EndpointVisibility(visibility))
		if url == nil {
			return nil, fmt.Errorf("endpoint %q not yet resolved for visibility %q", epName, visibility)
		}
		return url, nil
	}
	return nil, fmt.Errorf("endpoint %q not found on %s/%s in %s", epName, project, component, env)
}

// resolveResource finds the provider ResourceReleaseBinding, verifies it is Ready, and
// derives the dial target from the value-kind `host`/`port` outputs (convention, D12).
func (h *DevConnectHandler) resolveResource(ctx context.Context, ns, project string, dep devconnect.ResourceDep, env string) (*devconnect.ResourceRender, devconnect.Target, error) {
	key := "res/" + dep.Ref
	rrb, err := h.findResourceReleaseBinding(ctx, ns, project, dep.Ref, env)
	if err != nil {
		return nil, devconnect.Target{}, err
	}
	if !isResourceReleaseBindingReady(rrb) {
		return nil, devconnect.Target{}, fmt.Errorf("resource %q is not ready in %s", dep.Ref, env)
	}

	outputs := make(map[string]openchoreov1alpha1.ResolvedResourceOutput, len(rrb.Status.Outputs))
	for _, o := range rrb.Status.Outputs {
		outputs[o.Name] = o
	}

	host, hostOK := outputs["host"]
	port, portOK := outputs["port"]
	if !hostOK || !portOK || isSecretOutput(host) || isSecretOutput(port) {
		return nil, devconnect.Target{}, fmt.Errorf("resource %q has no discrete value-kind host/port outputs; connectivity needs secret resolution (phase 2)", dep.Ref)
	}
	portNum, err := strconv.Atoi(port.Value)
	if err != nil {
		return nil, devconnect.Target{}, fmt.Errorf("resource %q port output %q is not numeric", dep.Ref, port.Value)
	}

	render := &devconnect.ResourceRender{
		HostEnv:   dep.EnvBindings["host"],
		PortEnv:   dep.EnvBindings["port"],
		StaticEnv: map[string]string{},
	}
	for outputName, envVar := range dep.EnvBindings {
		if outputName == "host" || outputName == "port" {
			continue
		}
		out, ok := outputs[outputName]
		if !ok {
			continue // declared binding for an output the provider didn't emit; skip
		}
		if isSecretOutput(out) {
			render.OmittedSecretEnv = append(render.OmittedSecretEnv, envVar) // phase 2
			continue
		}
		render.StaticEnv[envVar] = out.Value
	}

	target := devconnect.Target{Key: key, Proto: "tcp", Host: host.Value, Port: portNum}
	return render, target, nil
}

// findReleaseBinding lists ReleaseBindings in the namespace and returns the single one
// matching (project, component, environment) that is not being undeployed. The API
// server's client is uncached with no field indexes, so we list + filter in memory.
func (h *DevConnectHandler) findReleaseBinding(ctx context.Context, ns, project, component, env string) (*openchoreov1alpha1.ReleaseBinding, error) {
	var list openchoreov1alpha1.ReleaseBindingList
	if err := h.k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list release bindings: %w", err)
	}
	var match *openchoreov1alpha1.ReleaseBinding
	for i := range list.Items {
		rb := &list.Items[i]
		if rb.Spec.Owner.ProjectName != project || rb.Spec.Owner.ComponentName != component || rb.Spec.Environment != env {
			continue
		}
		if rb.Spec.State == openchoreov1alpha1.ReleaseStateUndeploy {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple release bindings match %s/%s in %s", project, component, env)
		}
		match = rb
	}
	if match == nil {
		return nil, fmt.Errorf("no release binding for %s/%s in %s", project, component, env)
	}
	return match, nil
}

func (h *DevConnectHandler) findResourceReleaseBinding(ctx context.Context, ns, project, resource, env string) (*openchoreov1alpha1.ResourceReleaseBinding, error) {
	var list openchoreov1alpha1.ResourceReleaseBindingList
	if err := h.k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list resource release bindings: %w", err)
	}
	var match *openchoreov1alpha1.ResourceReleaseBinding
	for i := range list.Items {
		rrb := &list.Items[i]
		if rrb.Spec.Owner.ProjectName != project || rrb.Spec.Owner.ResourceName != resource || rrb.Spec.Environment != env {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple resource release bindings match %s/%s in %s", project, resource, env)
		}
		match = rrb
	}
	if match == nil {
		return nil, fmt.Errorf("no resource release binding for %s/%s in %s", project, resource, env)
	}
	return match, nil
}

// urlForVisibility mirrors the controller's resolveURLForVisibility: project/namespace
// visibility resolve to the in-cluster ServiceURL; external resolves to a gateway URL.
func urlForVisibility(ep openchoreov1alpha1.EndpointURLStatus, visibility openchoreov1alpha1.EndpointVisibility) *openchoreov1alpha1.EndpointURL {
	switch visibility {
	case openchoreov1alpha1.EndpointVisibilityProject, openchoreov1alpha1.EndpointVisibilityNamespace:
		return ep.ServiceURL
	case openchoreov1alpha1.EndpointVisibilityExternal:
		if ep.ExternalURLs != nil {
			if ep.ExternalURLs.HTTPS != nil {
				return ep.ExternalURLs.HTTPS
			}
			if ep.ExternalURLs.HTTP != nil {
				return ep.ExternalURLs.HTTP
			}
			if ep.ExternalURLs.TLS != nil {
				return ep.ExternalURLs.TLS
			}
		}
		return nil
	default:
		return nil
	}
}

func isSecretOutput(o openchoreov1alpha1.ResolvedResourceOutput) bool {
	return o.SecretKeyRef != nil || o.ConfigMapKeyRef != nil
}

// isResourceReleaseBindingReady mirrors the consumer controller's readiness gate: the
// aggregate Ready condition is True and observed the current generation.
func isResourceReleaseBindingReady(rrb *openchoreov1alpha1.ResourceReleaseBinding) bool {
	cond := meta.FindStatusCondition(rrb.Status.Conditions, "Ready")
	return cond != nil && cond.Status == metav1.ConditionTrue && cond.ObservedGeneration == rrb.Generation
}

// capabilitySigner mints capability JWTs.
type capabilitySigner struct {
	privKey  ed25519.PrivateKey
	keyID    string
	issuer   string
	audience string
	ttl      time.Duration
}

func (s *capabilitySigner) sign(subject string, comp devconnect.ComponentRef, env string, targets []devconnect.Target) (string, error) {
	now := time.Now()
	claims := &devconnect.CapabilityClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{s.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
		Component: comp,
		Env:       env,
		Targets:   targets,
	}
	return devconnect.SignCapability(claims, s.privKey, s.keyID)
}

func loadEd25519PrivateKeyPEM(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block in signing key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS8 private key: %w", err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key is %T, want ed25519", parsed)
	}
	return priv, nil
}
