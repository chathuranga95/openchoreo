// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package legacymcphandlers

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/labels"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/models"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

func (h *MCPHandler) ListNamespaces(ctx context.Context) (any, error) {
	result, err := h.services.NamespaceService.ListNamespaces(ctx, services.ListOptions{})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (h *MCPHandler) GetNamespace(ctx context.Context, name string) (any, error) {
	return h.services.NamespaceService.GetNamespace(ctx, name)
}

func (h *MCPHandler) CreateNamespace(ctx context.Context, req *models.CreateNamespaceRequest) (any, error) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: req.Name,
			Labels: map[string]string{
				labels.LabelKeyControlPlaneNamespace: labels.LabelValueTrue,
			},
			Annotations: make(map[string]string),
		},
	}

	if req.DisplayName != "" {
		ns.Annotations[controller.AnnotationKeyDisplayName] = req.DisplayName
	}
	if req.Description != "" {
		ns.Annotations[controller.AnnotationKeyDescription] = req.Description
	}

	return h.services.NamespaceService.CreateNamespace(ctx, ns)
}

func (h *MCPHandler) ListSecretReferences(ctx context.Context, namespaceName string) (any, error) {
	result, err := h.services.SecretReferenceService.ListSecretReferences(ctx, namespaceName, services.ListOptions{})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}
