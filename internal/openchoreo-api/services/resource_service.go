// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"context"
	"encoding/json"
	"fmt"

	"golang.org/x/exp/slog"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ApplyResourceResult represents the result of applying a resource
type ApplyResourceResult struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	Operation  string `json:"operation"` // "created" or "updated"
}

type ResourceService struct {
	k8sClient client.Client
	logger    *slog.Logger
}

func NewResourceService(k8sClient client.Client, logger *slog.Logger) *ResourceService {
	return &ResourceService{
		k8sClient: k8sClient,
		logger:    logger,
	}
}

// ApplyResourceFromJSON applies a resource from YAML definition
func (s *ResourceService) ApplyResourceFromJSON(ctx context.Context, jsonContent string) (*ApplyResourceResult, error) {
	s.logger.Debug("Applying resource from JSON")

	// Parse JSON into map
	var resourceObj map[string]interface{}
	if err := json.Unmarshal([]byte(jsonContent), &resourceObj); err != nil {
		s.logger.Error("Failed to parse resource JSON", "error", err)
		return nil, fmt.Errorf("failed to parse resource: %w", err)
	}

	// Validate required fields
	kind, apiVersion, name, err := s.validateResource(resourceObj)
	if err != nil {
		return nil, err
	}

	// Convert to unstructured object
	unstructuredObj := &unstructured.Unstructured{Object: resourceObj}

	// Handle namespace logic
	if err := s.handleResourceNamespace(unstructuredObj, apiVersion, kind); err != nil {
		s.logger.Error("Failed to handle resource namespace",
			"kind", kind, "name", name, "error", err)
		return nil, fmt.Errorf("failed to handle resource namespace: %w", err)
	}

	// Apply the resource to Kubernetes
	operation, err := s.applyToKubernetes(ctx, unstructuredObj)
	if err != nil {
		s.logger.Error("Failed to apply resource to Kubernetes",
			"kind", kind, "name", name, "error", err)
		return nil, fmt.Errorf("failed to apply resource: %w", err)
	}

	result := &ApplyResourceResult{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       name,
		Namespace:  unstructuredObj.GetNamespace(),
		Operation:  operation,
	}

	s.logger.Info("Resource applied successfully",
		"kind", kind, "name", name, "namespace", unstructuredObj.GetNamespace(), "operation", operation)

	return result, nil
}

// validateResource validates the resource has required fields
func (s *ResourceService) validateResource(resourceObj map[string]interface{}) (string, string, string, error) {
	// Validate kind
	kind, ok := resourceObj["kind"].(string)
	if !ok || kind == "" {
		return "", "", "", fmt.Errorf("missing or invalid 'kind' field")
	}

	// Validate apiVersion
	apiVersion, ok := resourceObj["apiVersion"].(string)
	if !ok || apiVersion == "" {
		return "", "", "", fmt.Errorf("missing or invalid 'apiVersion' field")
	}

	// Parse and validate the group from apiVersion
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid apiVersion format '%s': %w", apiVersion, err)
	}

	// Check if the resource belongs to openchoreo.dev group
	if gv.Group != openchoreoGroup {
		return "", "", "", fmt.Errorf("only resources with 'openchoreo.dev' group are supported, got '%s'", gv.Group)
	}

	// Validate metadata
	metadata, ok := resourceObj["metadata"].(map[string]interface{})
	if !ok {
		return "", "", "", fmt.Errorf("missing or invalid 'metadata' field")
	}

	// Validate name
	name, ok := metadata["name"].(string)
	if !ok || name == "" {
		return "", "", "", fmt.Errorf("missing or invalid 'metadata.name' field")
	}

	return kind, apiVersion, name, nil
}

// handleResourceNamespace handles namespace logic for the resource
func (s *ResourceService) handleResourceNamespace(obj *unstructured.Unstructured, apiVersion, kind string) error {
	gvk := obj.GroupVersionKind()
	if gvk.Empty() {
		// Parse the apiVersion and kind to create GVK
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return fmt.Errorf("invalid apiVersion: %w", err)
		}
		gvk = schema.GroupVersionKind{
			Group:   gv.Group,
			Version: gv.Version,
			Kind:    kind,
		}
		obj.SetGroupVersionKind(gvk)
	}

	// Check if the resource is namespaced by querying the API
	// For now, we'll use a simple heuristic: cluster-scoped resources typically include
	// Organization, DataPlane, BuildPlane, ComponentTypeDefinition, Addon
	clusterScopedKinds := map[string]bool{
		"Organization":             true,
		"DataPlane":                true,
		"BuildPlane":               true,
		"ComponentTypeDefinition":  true,
		"Addon":                    true,
		"ServiceClass":             true,
		"WebApplicationClass":      true,
		"ScheduledTaskClass":       true,
		"APIClass":                 true,
		"ConfigurationGroup":       true,
		"ClusterWorkflowTemplate":  true,
		"CustomResourceDefinition": true,
	}

	if clusterScopedKinds[kind] {
		// Cluster-scoped resource - should not have namespace
		if obj.GetNamespace() != "" {
			s.logger.Warn("Removing namespace from cluster-scoped resource",
				"kind", kind, "name", obj.GetName(), "namespace", obj.GetNamespace())
			obj.SetNamespace("")
		}
		return nil
	}

	// Namespaced resource
	return s.handleNamespacedResource(obj, gvk)
}

// handleNamespacedResource handles namespace defaulting for namespaced resources
func (s *ResourceService) handleNamespacedResource(obj *unstructured.Unstructured, gvk schema.GroupVersionKind) error {
	// If namespace is already set, keep it
	if obj.GetNamespace() != "" {
		return nil
	}

	// Apply default namespace
	defaultNamespace := "default"
	obj.SetNamespace(defaultNamespace)
	s.logger.Info("Applied default namespace to resource",
		"kind", gvk.Kind, "name", obj.GetName(), "namespace", defaultNamespace)

	return nil
}

// applyToKubernetes applies the resource to Kubernetes cluster
func (s *ResourceService) applyToKubernetes(ctx context.Context, obj *unstructured.Unstructured) (string, error) {
	// Create a unique field manager
	fieldManager := "openchoreo-mcp"

	// Check if the resource already exists
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())

	err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}, existing)

	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return "", err
		}
		// Resource doesn't exist, create it
		if err := s.k8sClient.Create(ctx, obj); err != nil {
			return "", err
		}
		return "created", nil
	}

	// Resource exists, perform server-side apply (patch)
	patch := client.Apply
	patchOptions := []client.PatchOption{
		client.ForceOwnership,
		client.FieldOwner(fieldManager),
	}

	if err := s.k8sClient.Patch(ctx, obj, patch, patchOptions...); err != nil {
		return "", err
	}

	return "updated", nil
}

// GetResourceResult represents the result of getting a resource
type GetResourceResult struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Name       string                 `json:"name"`
	Namespace  string                 `json:"namespace,omitempty"`
	Spec       map[string]interface{} `json:"spec,omitempty"`
	Status     map[string]interface{} `json:"status,omitempty"`
}

// GetResourceFromKind retrieves a resource by kind, name, and namespace
func (s *ResourceService) GetResourceFromKind(ctx context.Context, kind, name, namespace string) (*GetResourceResult, error) {
	s.logger.Debug("Getting resource", "kind", kind, "name", name, "namespace", namespace)

	// Validate inputs
	if kind == "" {
		return nil, fmt.Errorf("kind is required")
	}
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}

	// Create unstructured object with openchoreo.dev group
	obj := &unstructured.Unstructured{}
	gvk := schema.GroupVersionKind{
		Group:   openchoreoGroup,
		Version: "v1alpha1", // Default to v1alpha1
		Kind:    kind,
	}
	obj.SetGroupVersionKind(gvk)

	// Get the resource from Kubernetes
	err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, obj)

	if err != nil {
		s.logger.Error("Failed to get resource", "kind", kind, "name", name, "namespace", namespace, "error", err)
		return nil, fmt.Errorf("failed to get resource: %w", err)
	}

	// Extract spec and status
	spec, _ := obj.Object["spec"].(map[string]interface{})
	status, _ := obj.Object["status"].(map[string]interface{})

	result := &GetResourceResult{
		APIVersion: obj.GetAPIVersion(),
		Kind:       obj.GetKind(),
		Name:       obj.GetName(),
		Namespace:  obj.GetNamespace(),
		Spec:       spec,
		Status:     status,
	}

	s.logger.Info("Resource retrieved successfully", "kind", kind, "name", name, "namespace", obj.GetNamespace())

	return result, nil
}

// DeleteResourceResult represents the result of deleting a resource
type DeleteResourceResult struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	Message    string `json:"message"`
}

// DeleteResourceFromKind deletes a resource by kind, name, and namespace
func (s *ResourceService) DeleteResourceFromKind(ctx context.Context, kind, name, namespace string) (*DeleteResourceResult, error) {
	s.logger.Debug("Deleting resource", "kind", kind, "name", name, "namespace", namespace)

	// Validate inputs
	if kind == "" {
		return nil, fmt.Errorf("kind is required")
	}
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}

	// Validate that the resource kind is from openchoreo.dev group
	// Create unstructured object with openchoreo.dev group
	obj := &unstructured.Unstructured{}
	gvk := schema.GroupVersionKind{
		Group:   openchoreoGroup,
		Version: "v1alpha1", // Default to v1alpha1
		Kind:    kind,
	}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(name)
	obj.SetNamespace(namespace)

	// Delete the resource from Kubernetes
	err := s.k8sClient.Delete(ctx, obj)
	if err != nil {
		s.logger.Error("Failed to delete resource", "kind", kind, "name", name, "namespace", namespace, "error", err)
		return nil, fmt.Errorf("failed to delete resource: %w", err)
	}

	apiVersion := openchoreoGroup + "/v1alpha1"
	result := &DeleteResourceResult{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       name,
		Namespace:  namespace,
		Message:    fmt.Sprintf("Resource %s/%s deleted successfully", kind, name),
	}

	s.logger.Info("Resource deleted successfully", "kind", kind, "name", name, "namespace", namespace)

	return result, nil
}

// ListResourcesResult represents the result of listing resources
type ListResourcesResult struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Items      []ResourceSummary `json:"items"`
	TotalCount int               `json:"totalCount"`
}

// ResourceSummary provides a summary of a resource
type ResourceSummary struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace,omitempty"`
	CreatedAt string                 `json:"createdAt,omitempty"`
	Labels    map[string]string      `json:"labels,omitempty"`
	Status    map[string]interface{} `json:"status,omitempty"`
}

// ListResourcesFromKind lists all resources of a given kind
func (s *ResourceService) ListResourcesFromKind(ctx context.Context, kind, namespace string) (*ListResourcesResult, error) {
	s.logger.Debug("Listing resources", "kind", kind, "namespace", namespace)

	// Validate inputs
	if kind == "" {
		return nil, fmt.Errorf("kind is required")
	}

	// Create unstructured list with openchoreo.dev group
	list := &unstructured.UnstructuredList{}
	gvk := schema.GroupVersionKind{
		Group:   openchoreoGroup,
		Version: "v1alpha1", // Default to v1alpha1
		Kind:    kind,
	}
	list.SetGroupVersionKind(gvk)

	// Prepare list options
	var listOptions []client.ListOption
	if namespace != "" {
		listOptions = append(listOptions, client.InNamespace(namespace))
	}

	// List the resources from Kubernetes
	err := s.k8sClient.List(ctx, list, listOptions...)
	if err != nil {
		s.logger.Error("Failed to list resources", "kind", kind, "namespace", namespace, "error", err)
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	// Convert items to summary format
	items := make([]ResourceSummary, 0, len(list.Items))
	for _, item := range list.Items {
		summary := ResourceSummary{
			Name:      item.GetName(),
			Namespace: item.GetNamespace(),
			Labels:    item.GetLabels(),
		}

		// Get creation timestamp
		creationTime := item.GetCreationTimestamp()
		if !creationTime.Time.IsZero() {
			summary.CreatedAt = creationTime.Format("2006-01-02T15:04:05Z")
		}

		// Extract status if available
		if status, ok := item.Object["status"].(map[string]interface{}); ok {
			summary.Status = status
		}

		items = append(items, summary)
	}

	apiVersion := openchoreoGroup + "/v1alpha1"
	result := &ListResourcesResult{
		APIVersion: apiVersion,
		Kind:       kind,
		Items:      items,
		TotalCount: len(items),
	}

	s.logger.Info("Resources listed successfully", "kind", kind, "namespace", namespace, "count", len(items))

	return result, nil
}
