// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package legacymcphandlers

import (
	"context"

	"github.com/openchoreo/openchoreo/internal/openchoreo-api/models"
)

type ListBuildTemplatesResponse struct {
	Workflows []*models.WorkflowResponse `json:"workflows"`
}

type ListBuildsResponse struct {
	WorkflowRuns []models.ComponentWorkflowResponse `json:"workflowRuns"`
}

// ListBuildTemplates lists available build templates (component workflows) in a namespace.
func (h *LegacyMCPHandler) ListBuildTemplates(ctx context.Context, namespaceName string) (any, error) {
	workflows, err := h.Services.ComponentWorkflowService.ListComponentWorkflows(ctx, namespaceName)
	if err != nil {
		return ListBuildTemplatesResponse{}, err
	}
	return ListBuildTemplatesResponse{
		Workflows: workflows,
	}, nil
}

// TriggerBuild triggers a build for a component at a specific commit.
func (h *LegacyMCPHandler) TriggerBuild(ctx context.Context, namespaceName, projectName, componentName, commit string) (any, error) {
	return h.Services.ComponentWorkflowService.TriggerWorkflow(ctx, namespaceName, projectName, componentName, commit)
}

// ListBuilds lists all builds (component workflow runs) for a component.
func (h *LegacyMCPHandler) ListBuilds(ctx context.Context, namespaceName, projectName, componentName string) (any, error) {
	workflowRuns, err := h.Services.ComponentWorkflowService.ListComponentWorkflowRuns(ctx, namespaceName, projectName, componentName)
	if err != nil {
		return ListBuildsResponse{}, err
	}
	return ListBuildsResponse{
		WorkflowRuns: workflowRuns,
	}, nil
}
