// Copyright 2026 The OpenChoreo Authors
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authz "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/depconnect"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/config"
	svcpkg "github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	authmw "github.com/openchoreo/openchoreo/internal/server/middleware/auth"
)

// DepConnectHandler serves POST /api/v1/dev/connect:resolve — it resolves a workload's
// declared dependencies against provider status and returns connection targets plus a
// short-lived capability (worklog §8). The capability is minted and verified by
// openchoreo-api itself: occ presents it back to DepConnectStreamHandler when it opens
// a tunnel for one of the targets, which relays the stream through the existing
// cluster-gateway/cluster-agent management tunnel — there is no separate dev-tunnel
// agent to distribute a verification key to. v1 = connectivity only: it emits
// non-secret env and skips secret-backed outputs (phase 2).
type DepConnectHandler struct {
	k8sClient    client.Client
	authzChecker *svcpkg.AuthzChecker
	signer       *capabilitySigner
	logger       *slog.Logger
}

// NewDepConnectHandler loads the signing key and builds the handler.
func NewDepConnectHandler(k8sClient client.Client, authzChecker *svcpkg.AuthzChecker, cfg config.DepConnectConfig, logger *slog.Logger) (*DepConnectHandler, error) {
	priv, err := loadEd25519PrivateKeyPEM(cfg.SigningKeyPath)
	if err != nil {
		return nil, fmt.Errorf("dep-connect: load signing key: %w", err)
	}
	return &DepConnectHandler{
		k8sClient:    k8sClient,
		authzChecker: authzChecker,
		signer: &capabilitySigner{
			privKey: priv,
			keyID:   cfg.KeyID,
			issuer:  cfg.Issuer,
			ttl:     time.Duration(cfg.TTLSeconds) * time.Second,
		},
		logger: logger.With("component", "dep-connect-handler"),
	}, nil
}

// VerifyKey returns the Ed25519 public key that verifies capabilities this handler
// signs. Capabilities are minted and verified in the same process, so the verify key
// is simply the public half of the signing key.
func (h *DepConnectHandler) VerifyKey() ed25519.PublicKey {
	return h.signer.privKey.Public().(ed25519.PublicKey)
}

// ServeHTTP handles the resolve request.
func (h *DepConnectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req depconnect.ResolveRequest
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
func (h *DepConnectHandler) resolve(ctx context.Context, req depconnect.ResolveRequest, subject string) (*depconnect.ResolveResponse, error) {
	// Every dependency for a given environment resolves through the same data
	// plane (an Environment has exactly one DataPlaneRef), so this runs once per
	// request rather than once per dependency.
	plane, err := h.resolveDepConnectPlane(ctx, req.Namespace, req.Environment)
	if err != nil {
		return nil, fmt.Errorf("resolve data plane for environment %q: %w", req.Environment, err)
	}

	var (
		capTargets   []depconnect.Target
		respTargets  []depconnect.ResolvedTarget
		unconnutable []depconnect.Unconnectable
	)

	for _, dep := range req.Endpoints {
		key := "ep/" + dep.Component + "/" + dep.Name
		providerProject := dep.Project
		if providerProject == "" {
			providerProject = req.Project
		}
		url, err := h.resolveEndpoint(ctx, req.Namespace, providerProject, dep.Component, dep.Name, dep.Visibility, req.Environment)
		if err != nil {
			unconnutable = append(unconnutable, depconnect.Unconnectable{Ref: key, Reason: err.Error()})
			continue
		}
		capTargets = append(capTargets, depconnect.Target{
			Key: key, Proto: "tcp", Host: url.Host, Port: int(url.Port),
			PlaneType: plane.planeType, PlaneID: plane.planeID, CRNamespace: plane.crNamespace, CRName: plane.crName,
		})
		respTargets = append(respTargets, depconnect.ResolvedTarget{
			Key:      key,
			Proto:    "tcp",
			Endpoint: &depconnect.EndpointRender{Scheme: url.Scheme, BasePath: url.Path, Bindings: dep.EnvBindings},
		})
	}

	for _, dep := range req.Resources {
		render, target, err := h.resolveResource(ctx, req.Namespace, req.Project, dep, req.Environment, plane)
		if err != nil {
			unconnutable = append(unconnutable, depconnect.Unconnectable{Ref: dep.Ref, Reason: err.Error()})
			continue
		}
		capTargets = append(capTargets, target)
		respTargets = append(respTargets, depconnect.ResolvedTarget{Key: target.Key, Proto: "tcp", Resource: render})
	}

	capability, err := h.signer.sign(subject, req.Namespace, depconnect.ComponentRef{Project: req.Project, Name: req.Component}, req.Environment, capTargets)
	if err != nil {
		return nil, fmt.Errorf("sign capability: %w", err)
	}

	return &depconnect.ResolveResponse{
		Capability:    capability,
		Targets:       respTargets,
		Unconnectable: unconnutable,
	}, nil
}

// resolveDepConnectPlane resolves the data plane serving env, reusing the same
// DataPlane/ClusterDataPlane mapping ExecHandler uses for `occ exec`.
func (h *DepConnectHandler) resolveDepConnectPlane(ctx context.Context, ns, envName string) (execPlaneInfo, error) {
	env := &openchoreov1alpha1.Environment{}
	if err := h.k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: envName}, env); err != nil {
		if apierrors.IsNotFound(err) {
			return execPlaneInfo{}, fmt.Errorf("environment %q not found in namespace %q", envName, ns)
		}
		return execPlaneInfo{}, fmt.Errorf("failed to look up environment %q: %w", envName, err)
	}
	if env.Spec.DataPlaneRef == nil {
		return execPlaneInfo{}, fmt.Errorf("environment %q has no data plane reference", envName)
	}

	dpResult, err := controller.GetDataPlaneFromRef(ctx, h.k8sClient, env.Namespace, env.Spec.DataPlaneRef)
	if err != nil {
		return execPlaneInfo{}, fmt.Errorf("failed to resolve data plane: %w", err)
	}

	plane := resolveExecPlaneInfo(dpResult)
	if plane.planeID == "" {
		return execPlaneInfo{}, fmt.Errorf("failed to determine plane ID for environment %q", envName)
	}
	return plane, nil
}

// resolveEndpoint finds the provider ReleaseBinding and reads the named endpoint's
// in-cluster ServiceURL (for project/namespace visibility).
func (h *DepConnectHandler) resolveEndpoint(ctx context.Context, ns, project, component, epName, visibility, env string) (*openchoreov1alpha1.EndpointURL, error) {
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
func (h *DepConnectHandler) resolveResource(ctx context.Context, ns, project string, dep depconnect.ResourceDep, env string, plane execPlaneInfo) (*depconnect.ResourceRender, depconnect.Target, error) {
	key := "res/" + dep.Ref
	rrb, err := h.findResourceReleaseBinding(ctx, ns, project, dep.Ref, env)
	if err != nil {
		return nil, depconnect.Target{}, err
	}
	if !isResourceReleaseBindingReady(rrb) {
		return nil, depconnect.Target{}, fmt.Errorf("resource %q is not ready in %s", dep.Ref, env)
	}

	outputs := make(map[string]openchoreov1alpha1.ResolvedResourceOutput, len(rrb.Status.Outputs))
	for _, o := range rrb.Status.Outputs {
		outputs[o.Name] = o
	}

	host, hostOK := outputs["host"]
	port, portOK := outputs["port"]
	if !hostOK || !portOK || isSecretOutput(host) || isSecretOutput(port) {
		return nil, depconnect.Target{}, fmt.Errorf("resource %q has no discrete value-kind host/port outputs; connectivity needs secret resolution (phase 2)", dep.Ref)
	}
	portNum, err := strconv.Atoi(port.Value)
	if err != nil {
		return nil, depconnect.Target{}, fmt.Errorf("resource %q port output %q is not numeric", dep.Ref, port.Value)
	}

	render := &depconnect.ResourceRender{
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

	target := depconnect.Target{
		Key: key, Proto: "tcp", Host: host.Value, Port: portNum,
		PlaneType: plane.planeType, PlaneID: plane.planeID, CRNamespace: plane.crNamespace, CRName: plane.crName,
	}
	return render, target, nil
}

// findReleaseBinding lists ReleaseBindings in the namespace and returns the single one
// matching (project, component, environment) that is not being undeployed. The API
// server's client is uncached with no field indexes, so we list + filter in memory.
func (h *DepConnectHandler) findReleaseBinding(ctx context.Context, ns, project, component, env string) (*openchoreov1alpha1.ReleaseBinding, error) {
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

func (h *DepConnectHandler) findResourceReleaseBinding(ctx context.Context, ns, project, resource, env string) (*openchoreov1alpha1.ResourceReleaseBinding, error) {
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
	privKey ed25519.PrivateKey
	keyID   string
	issuer  string
	ttl     time.Duration
}

func (s *capabilitySigner) sign(subject, namespace string, comp depconnect.ComponentRef, env string, targets []depconnect.Target) (string, error) {
	now := time.Now()
	claims := &depconnect.CapabilityClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{depconnect.CapabilityAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
		Namespace: namespace,
		Component: comp,
		Env:       env,
		Targets:   targets,
	}
	return depconnect.SignCapability(claims, s.privKey, s.keyID)
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
