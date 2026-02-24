// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package legacymcphandlers

import (
	"context"
)

func (h *LegacyMCPHandler) GetProjectDeploymentPipeline(ctx context.Context, namespaceName, projectName string) (any, error) {
	return h.Services.DeploymentPipelineService.GetProjectDeploymentPipeline(ctx, namespaceName, projectName)
}
