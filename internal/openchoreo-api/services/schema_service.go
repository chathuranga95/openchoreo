// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"context"
	"fmt"
	"sort"

	"golang.org/x/exp/slog"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openchoreo/openchoreo/internal/openchoreo-api/models"
)

const openchoreoGroup = "openchoreo.dev"

type SchemaService struct {
	k8sClient client.Client
	logger    *slog.Logger
}

func NewSchemaService(k8sClient client.Client, logger *slog.Logger) *SchemaService {
	return &SchemaService{
		k8sClient: k8sClient,
		logger:    logger,
	}
}

// ListCRDs retrieves all OpenChoreo CustomResourceDefinitions from the cluster
func (s *SchemaService) ListCRDs(ctx context.Context) ([]*models.CRDInfo, error) {
	s.logger.Debug("Listing CustomResourceDefinitions")

	var crdList apiextensionsv1.CustomResourceDefinitionList
	if err := s.k8sClient.List(ctx, &crdList); err != nil {
		s.logger.Error("Failed to list CRDs", "error", err)
		return nil, fmt.Errorf("failed to list CRDs: %w", err)
	}

	s.logger.Info("Found CRDs in cluster", "total", len(crdList.Items))

	// Filter and convert OpenChoreo CRDs
	crds := make([]*models.CRDInfo, 0)
	for i := range crdList.Items {
		crd := &crdList.Items[i]

		s.logger.Debug("Processing CRD", "name", crd.Name, "group", crd.Spec.Group)

		// Only include OpenChoreo CRDs
		if crd.Spec.Group != openchoreoGroup {
			continue
		}

		// Get the storage version (the one that's stored in etcd)
		storageVersion := ""
		for _, version := range crd.Spec.Versions {
			if version.Storage {
				storageVersion = version.Name
				break
			}
		}

		if storageVersion == "" && len(crd.Spec.Versions) > 0 {
			// Fallback to first version if no storage version is explicitly marked
			storageVersion = crd.Spec.Versions[0].Name
		}

		crdInfo := &models.CRDInfo{
			Kind:       crd.Spec.Names.Kind,
			Group:      crd.Spec.Group,
			Version:    storageVersion,
			Namespaced: crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
			Plural:     crd.Spec.Names.Plural,
			Singular:   crd.Spec.Names.Singular,
		}
		crds = append(crds, crdInfo)
	}

	// Sort by Kind for consistent output
	sort.Slice(crds, func(i, j int) bool {
		return crds[i].Kind < crds[j].Kind
	})

	s.logger.Debug("Listed CRDs", "count", len(crds))
	return crds, nil
}

// GetCRD retrieves a specific CustomResourceDefinition by name from the cluster
func (s *SchemaService) GetCRD(ctx context.Context, crdName string) (*models.CRDDetails, error) {
	s.logger.Debug("Getting CustomResourceDefinition", "name", crdName)

	var crd apiextensionsv1.CustomResourceDefinition
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: crdName}, &crd); err != nil {
		s.logger.Error("Failed to get CRD", "name", crdName, "error", err)
		return nil, fmt.Errorf("failed to get CRD %s: %w", crdName, err)
	}

	// Only allow OpenChoreo CRDs
	if crd.Spec.Group != openchoreoGroup {
		s.logger.Warn("Attempted to access non-OpenChoreo CRD", "name", crdName, "group", crd.Spec.Group)
		return nil, fmt.Errorf("CRD %s is not an OpenChoreo CRD", crdName)
	}

	// Get the storage version (the one that's stored in etcd)
	storageVersion := ""
	var storageVersionSpec *apiextensionsv1.CustomResourceDefinitionVersion
	for i := range crd.Spec.Versions {
		version := &crd.Spec.Versions[i]
		if version.Storage {
			storageVersion = version.Name
			storageVersionSpec = version
			break
		}
	}

	if storageVersion == "" && len(crd.Spec.Versions) > 0 {
		// Fallback to first version if no storage version is explicitly marked
		storageVersion = crd.Spec.Versions[0].Name
		storageVersionSpec = &crd.Spec.Versions[0]
	}

	// Extract the OpenAPI schema
	var schema map[string]interface{}
	if storageVersionSpec != nil && storageVersionSpec.Schema != nil && storageVersionSpec.Schema.OpenAPIV3Schema != nil {
		// Convert the OpenAPIV3Schema to a map
		schemaJSON := storageVersionSpec.Schema.OpenAPIV3Schema
		schema = convertOpenAPISchemaToMap(schemaJSON)
	}

	// Get description from the CRD annotations if available
	description := ""
	if crd.Annotations != nil {
		if desc, ok := crd.Annotations["description"]; ok {
			description = desc
		}
	}

	crdDetails := &models.CRDDetails{
		Name:        crd.Name,
		Kind:        crd.Spec.Names.Kind,
		Group:       crd.Spec.Group,
		Version:     storageVersion,
		Namespaced:  crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
		Plural:      crd.Spec.Names.Plural,
		Singular:    crd.Spec.Names.Singular,
		ShortNames:  crd.Spec.Names.ShortNames,
		Categories:  crd.Spec.Names.Categories,
		Schema:      schema,
		Description: description,
	}

	s.logger.Debug("Retrieved CRD", "name", crdName)
	return crdDetails, nil
}

// convertOpenAPISchemaToMap converts an OpenAPIV3Schema to a map
func convertOpenAPISchemaToMap(schema *apiextensionsv1.JSONSchemaProps) map[string]interface{} {
	result := make(map[string]interface{})

	if schema.Type != "" {
		result["type"] = schema.Type
	}
	if schema.Description != "" {
		result["description"] = schema.Description
	}
	if schema.Format != "" {
		result["format"] = schema.Format
	}
	if schema.Title != "" {
		result["title"] = schema.Title
	}
	if schema.Default != nil {
		result["default"] = schema.Default
	}
	if schema.Example != nil {
		result["example"] = schema.Example
	}
	if len(schema.Enum) > 0 {
		result["enum"] = schema.Enum
	}
	if len(schema.Required) > 0 {
		result["required"] = schema.Required
	}

	// Handle properties
	if len(schema.Properties) > 0 {
		properties := make(map[string]interface{})
		for propName, propSchema := range schema.Properties {
			properties[propName] = convertOpenAPISchemaToMap(&propSchema)
		}
		result["properties"] = properties
	}

	// Handle items (for arrays)
	if schema.Items != nil && schema.Items.Schema != nil {
		result["items"] = convertOpenAPISchemaToMap(schema.Items.Schema)
	}

	// Handle additional properties
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		result["additionalProperties"] = convertOpenAPISchemaToMap(schema.AdditionalProperties.Schema)
	}

	// Validation constraints
	if schema.MinLength != nil {
		result["minLength"] = *schema.MinLength
	}
	if schema.MaxLength != nil {
		result["maxLength"] = *schema.MaxLength
	}
	if schema.Pattern != "" {
		result["pattern"] = schema.Pattern
	}
	if schema.Minimum != nil {
		result["minimum"] = *schema.Minimum
	}
	if schema.Maximum != nil {
		result["maximum"] = *schema.Maximum
	}
	if schema.MinItems != nil {
		result["minItems"] = *schema.MinItems
	}
	if schema.MaxItems != nil {
		result["maxItems"] = *schema.MaxItems
	}
	if schema.UniqueItems {
		result["uniqueItems"] = schema.UniqueItems
	}

	return result
}
