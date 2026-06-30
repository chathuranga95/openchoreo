// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authz "github.com/openchoreo/openchoreo/internal/authz/core"
	svcpkg "github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

// ResolveDependenciesHandler resolves a workload's declared dependencies to
// concrete in-cluster endpoint addresses for a given environment. It is the
// control-plane half of `occ dev connect`: occ sends the workload's
// connections, gets back the in-cluster host:port for each, then opens an L4
// tunnel per dependency. Resolution is driven by request input (not a deployed
// consumer ReleaseBinding), so it works for a not-yet-deployed workload.
type ResolveDependenciesHandler struct {
	k8sClient    client.Client
	authzChecker *svcpkg.AuthzChecker
	logger       *slog.Logger
}

// NewResolveDependenciesHandler creates a new resolve-dependencies handler.
func NewResolveDependenciesHandler(k8sClient client.Client, authzChecker *svcpkg.AuthzChecker, logger *slog.Logger) *ResolveDependenciesHandler {
	return &ResolveDependenciesHandler{
		k8sClient:    k8sClient,
		authzChecker: authzChecker,
		logger:       logger.With("component", "resolve-dependencies-handler"),
	}
}

// resolveDependenciesRequest is the POST body.
type resolveDependenciesRequest struct {
	// Project is the consumer's project; used to default a connection's project.
	Project string `json:"project"`
	// Environment is the target environment to resolve dependencies in.
	Environment string `json:"environment"`
	// Connections are the workload's declared endpoint dependencies.
	Connections []openchoreov1alpha1.WorkloadConnection `json:"connections"`
}

// resolvedDependency is a single dependency resolved to an in-cluster address.
type resolvedDependency struct {
	Project     string                                   `json:"project"`
	Component   string                                   `json:"component"`
	Endpoint    string                                   `json:"endpoint"`
	Visibility  string                                   `json:"visibility"`
	Type        string                                   `json:"type,omitempty"`
	Scheme      string                                   `json:"scheme,omitempty"`
	Host        string                                   `json:"host"`
	Port        int32                                    `json:"port,omitempty"`
	Path        string                                   `json:"path,omitempty"`
	Address     string                                   `json:"address"`
	EnvBindings openchoreov1alpha1.ConnectionEnvBindings `json:"envBindings"`
}

// pendingDependency is a dependency that could not be resolved yet.
type pendingDependency struct {
	Project   string `json:"project"`
	Component string `json:"component"`
	Endpoint  string `json:"endpoint"`
	Reason    string `json:"reason"`
}

type resolveDependenciesResponse struct {
	Resolved []resolvedDependency `json:"resolved"`
	Pending  []pendingDependency  `json:"pending"`
}

// ServeHTTP handles POST /api/v1/namespaces/{namespace}/dependencies/resolve.
func (h *ResolveDependenciesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	namespace := r.PathValue("namespace")
	if namespace == "" {
		http.Error(w, "namespace is required", http.StatusBadRequest)
		return
	}

	var req resolveDependenciesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Environment == "" {
		http.Error(w, "environment is required", http.StatusBadRequest)
		return
	}

	logger := h.logger.With("namespace", namespace, "environment", req.Environment, "project", req.Project)
	logger.Info("Resolve dependencies request", "connections", len(req.Connections))

	if h.authzChecker == nil {
		http.Error(w, "authorization not configured", http.StatusInternalServerError)
		return
	}

	resp := resolveDependenciesResponse{}
	for _, conn := range req.Connections {
		project := conn.Project
		if project == "" {
			project = req.Project
		}

		// Authorize: the caller must be able to view the target component in
		// this environment before we reveal its in-cluster address.
		if err := h.authzChecker.Check(ctx, svcpkg.CheckRequest{
			Action:       authz.ActionViewComponent,
			ResourceType: "component",
			ResourceID:   conn.Component,
			Hierarchy: authz.ResourceHierarchy{
				Namespace: namespace,
				Project:   project,
			},
			Context: authz.Context{
				Resource: authz.ResourceAttribute{
					Environment: svcpkg.FormatDualScopedResourceName(namespace, req.Environment, false),
				},
			},
		}); err != nil {
			if errors.Is(err, svcpkg.ErrForbidden) {
				http.Error(w, "you do not have permission to resolve dependency "+conn.Component, http.StatusForbidden)
				return
			}
			logger.Error("Authorization check failed", "component", conn.Component, "error", err)
			http.Error(w, "authorization check failed", http.StatusInternalServerError)
			return
		}

		resolved, pending := h.resolveOne(ctx, namespace, project, req.Environment, conn)
		if pending != nil {
			resp.Pending = append(resp.Pending, *pending)
		} else if resolved != nil {
			resp.Resolved = append(resp.Resolved, *resolved)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Error("Failed to encode resolve response", "error", err)
	}
}

// resolveOne resolves a single connection to the provider's in-cluster endpoint
// by reading the provider ReleaseBinding's status. Mirrors the releasebinding
// controller's resolveConnection, but filters in-memory (the API client may not
// register the controller's field index).
func (h *ResolveDependenciesHandler) resolveOne(
	ctx context.Context,
	namespace, project, environment string,
	conn openchoreov1alpha1.WorkloadConnection,
) (*resolvedDependency, *pendingDependency) {
	pending := func(reason string) *pendingDependency {
		return &pendingDependency{Project: project, Component: conn.Component, Endpoint: conn.Name, Reason: reason}
	}

	var rbList openchoreov1alpha1.ReleaseBindingList
	if err := h.k8sClient.List(ctx, &rbList, client.InNamespace(namespace)); err != nil {
		return nil, pending("failed to list ReleaseBindings: " + err.Error())
	}

	var match *openchoreov1alpha1.ReleaseBinding
	for i := range rbList.Items {
		rb := &rbList.Items[i]
		if rb.Spec.Owner.ProjectName == project &&
			rb.Spec.Owner.ComponentName == conn.Component &&
			rb.Spec.Environment == environment {
			if match != nil {
				return nil, pending("multiple ReleaseBindings found for " + project + "/" + conn.Component + " in " + environment)
			}
			match = rb
		}
	}
	if match == nil {
		return nil, pending("ReleaseBinding not found for " + project + "/" + conn.Component + " in " + environment)
	}
	if match.Spec.State == openchoreov1alpha1.ReleaseStateUndeploy {
		return nil, pending("component is undeployed")
	}

	for _, ep := range match.Status.Endpoints {
		if ep.Name != conn.Name {
			continue
		}
		// WorkloadConnection visibility is project|namespace, both served by the
		// in-cluster ServiceURL.
		if ep.ServiceURL == nil {
			return nil, pending("endpoint \"" + conn.Name + "\" has no in-cluster service URL yet")
		}
		url := ep.ServiceURL
		return &resolvedDependency{
			Project:     project,
			Component:   conn.Component,
			Endpoint:    conn.Name,
			Visibility:  conn.Visibility,
			Type:        string(ep.Type),
			Scheme:      url.Scheme,
			Host:        url.Host,
			Port:        url.Port,
			Path:        url.Path,
			Address:     formatEndpointAddr(*url),
			EnvBindings: conn.EnvBindings,
		}, nil
	}

	return nil, pending("endpoint \"" + conn.Name + "\" not yet resolved on provider")
}

// formatEndpointAddr mirrors the releasebinding controller's formatEndpointAddress:
// scheme://host:port/path for URL-style schemes, host:port otherwise.
func formatEndpointAddr(u openchoreov1alpha1.EndpointURL) string {
	var sb strings.Builder
	switch u.Scheme {
	case "http", "https", "ws", "wss", "tls":
		sb.WriteString(u.Scheme)
		sb.WriteString("://")
	}
	sb.WriteString(u.Host)
	if u.Port != 0 {
		sb.WriteString(":")
		sb.WriteString(strconv.Itoa(int(u.Port)))
	}
	if u.Path != "" {
		if !strings.HasPrefix(u.Path, "/") {
			sb.WriteString("/")
		}
		sb.WriteString(u.Path)
	}
	return sb.String()
}
