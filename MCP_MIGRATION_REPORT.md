# MCP Server Migration Report

## Summary

Migrated the MCP (Model Context Protocol) server from the legacy service layer (`legacyservices.Services`) to the new Kubernetes-native service layer (`handlerservices.Services`). The new MCP handler is a thin protocol-conversion adapter — it receives MCP tool parameters, converts them to CRD objects, calls the appropriate service, and returns the result.

**Before:** 69 tools across 8 toolsets, backed by legacy services
**After:** 44 tools across 4 toolsets, backed by new K8s-native services

---

## Removed Toolsets (4 entire toolsets)

### Build Toolset (5 tools removed)

| Tool | Description |
|------|-------------|
| `list_build_templates` | List available build templates in a namespace |
| `trigger_build` | Trigger a new build for a component at a specific commit |
| `list_builds` | List all builds for a component |
| `get_build_observer_url` | Get observability dashboard URL for component builds |
| `list_buildplanes` | List all build planes in a namespace |

**Reason:** No new service layer backing for build operations (Argo Workflows integration).

### Deployment Toolset (2 tools removed)

| Tool | Description |
|------|-------------|
| `get_deployment_pipeline` | Get deployment pipeline configuration for a project |
| `get_component_observer_url` | Get observability dashboard URL for a deployed component in an environment |

**Reason:** No new service layer backing for deployment pipeline retrieval and observer URL resolution.

### Schema Toolset (1 tool removed)

| Tool | Description |
|------|-------------|
| `explain_schema` | Explain the schema definition of a Kubernetes resource in JSON format |

**Reason:** No new service layer backing for generic schema explanation.

### Resource Toolset (3 tools removed)

| Tool | Description |
|------|-------------|
| `apply_resource` | Apply a Kubernetes resource to the cluster (server-side apply) |
| `delete_resource` | Delete a Kubernetes resource from the cluster |
| `get_resource` | Get a Kubernetes resource by kind and name from the cluster |

**Reason:** No new service layer backing for generic kubectl-like resource operations.

---

## Removed Tools from Retained Toolsets

### From Component Toolset (10 tools removed)

| Tool | Description | Reason |
|------|-------------|--------|
| `list_component_workflows` | List ComponentWorkflow templates | No workflow service in new layer |
| `get_component_workflow_schema` | Get workflow template schema | No workflow service in new layer |
| `trigger_component_workflow` | Trigger a workflow run for a component | No workflow service in new layer |
| `list_component_workflow_runs` | List workflow run executions | No workflow service in new layer |
| `update_component_workflow_schema` | Update workflow schema config | No workflow service in new layer |
| `list_component_traits` | List trait instances attached to a component | No component-level trait service |
| `update_component_traits` | Update all trait instances on a component | No component-level trait service |
| `update_component_binding` | Update a component binding's release state | Replaced by `patch_release_binding` |
| `get_component_release_schema` | Get release schema definition | No release schema service |
| `get_component_observer_url` | Get build observer URL (registered under component) | No observer URL service |

### From Infrastructure Toolset (4 tools removed)

| Tool | Description | Reason |
|------|-------------|--------|
| `list_workflows` | List workflows in a namespace | No workflow service in new layer |
| `get_workflow_schema` | Get workflow schema | No workflow service in new layer |
| `list_component_workflows_org_level` | List ComponentWorkflow templates (org-level) | No workflow service in new layer |
| `get_component_workflow_schema_org_level` | Get ComponentWorkflow schema (org-level) | No workflow service in new layer |

---

## Retained Toolsets and Tools (44 tools)

### Namespace Toolset (4 tools)

| Tool | Description |
|------|-------------|
| `list_namespaces` | List all namespaces |
| `get_namespace` | Get namespace details |
| `create_namespace` | Create a new namespace |
| `list_secret_references` | List secret references in a namespace |

### Project Toolset (3 tools)

| Tool | Description |
|------|-------------|
| `list_projects` | List projects in a namespace |
| `get_project` | Get project details |
| `create_project` | Create a new project |

### Component Toolset (15 tools)

| Tool | Description |
|------|-------------|
| `create_component` | Create a new component |
| `list_components` | List components in a project |
| `get_component` | Get detailed component info |
| `patch_component` | Partially update component config |
| `get_component_workloads` | Get real-time workload info |
| `list_component_releases` | List releases for a component |
| `create_component_release` | Create a release from latest build |
| `get_component_release` | Get specific release info |
| `get_component_schema` | Get component schema definition |
| `list_release_bindings` | List release bindings for a component |
| `patch_release_binding` | Update release binding configuration |
| `deploy_release` | Deploy a release to the lowest environment |
| `promote_component` | Promote a release to the next environment |
| `create_workload` | Create/update a component workload |
| `get_environment_release` | Get Release spec/status in an environment |

### Infrastructure Toolset (22 tools)

**Namespace-scoped (11 tools):**

| Tool | Description |
|------|-------------|
| `list_environments` | List environments in a namespace |
| `get_environment` | Get environment details |
| `create_environment` | Create a new environment |
| `list_dataplanes` | List data planes in a namespace |
| `get_dataplane` | Get data plane details |
| `create_dataplane` | Create a new data plane |
| `list_component_types` | List component types in a namespace |
| `get_component_type_schema` | Get component type schema |
| `list_traits` | List available traits |
| `get_trait_schema` | Get trait schema |
| `list_observability_planes` | List observability planes |

**Cluster-scoped (11 tools):**

| Tool | Description |
|------|-------------|
| `list_cluster_dataplanes` | List cluster-scoped data planes |
| `get_cluster_dataplane` | Get cluster data plane details |
| `create_cluster_dataplane` | Create a cluster-scoped data plane |
| `list_cluster_buildplanes` | List cluster-scoped build planes |
| `list_cluster_observability_planes` | List cluster observability planes |
| `list_cluster_component_types` | List cluster component types |
| `get_cluster_component_type` | Get cluster component type info |
| `get_cluster_component_type_schema` | Get cluster component type schema |
| `list_cluster_traits` | List cluster-scoped traits |
| `get_cluster_trait` | Get cluster trait info |
| `get_cluster_trait_schema` | Get cluster trait schema |

---

## Architecture Changes

### Before

```
MCPHandler
├── embeds LegacyMCPHandler (backed by legacyservices.Services)
├── shadows 4 methods with new service layer (namespaces only)
└── inherits ~65 methods from legacy handler
```

### After

```
MCPHandler
└── backed solely by handlerservices.Services (19 K8s-native services)
```

The new `MCPHandler` is a standalone struct with a single dependency:

```go
type MCPHandler struct {
    services *handlerservices.Services
}
```

### Handler Files Created

| File | Methods | Services Used |
|------|---------|---------------|
| `handler.go` | MCPHandler struct + constructor | — |
| `namespaces.go` | 4 methods | NamespaceService, SecretReferenceService |
| `projects.go` | 3 methods | ProjectService |
| `environments.go` | 3 methods | EnvironmentService |
| `dataplanes.go` | 3 methods | DataPlaneService |
| `components.go` | 15 methods | ComponentService, WorkloadService, ComponentReleaseService, ReleaseBindingService, ReleaseService |
| `infrastructure.go` | 5 methods | ComponentTypeService, TraitService, ObservabilityPlaneService |
| `cluster_resources.go` | 11 methods | ClusterDataPlaneService, ClusterBuildPlaneService, ClusterObservabilityPlaneService, ClusterComponentTypeService, ClusterTraitService |
| `helpers.go` | `derefInt32` helper | — |

### Legacy Handlers Preserved

The original MCP handlers were moved to `internal/openchoreo-api/legacymcphandlers/` for reference. They are no longer wired into the MCP server and can be removed once the migration is fully validated.

---

## Files Changed

### New Files (9)

- `internal/openchoreo-api/mcphandlers/handler.go`
- `internal/openchoreo-api/mcphandlers/namespaces.go`
- `internal/openchoreo-api/mcphandlers/projects.go`
- `internal/openchoreo-api/mcphandlers/environments.go`
- `internal/openchoreo-api/mcphandlers/dataplanes.go`
- `internal/openchoreo-api/mcphandlers/components.go`
- `internal/openchoreo-api/mcphandlers/infrastructure.go`
- `internal/openchoreo-api/mcphandlers/cluster_resources.go`
- `internal/openchoreo-api/mcphandlers/helpers.go`

### Archived (git mv)

- `internal/openchoreo-api/mcphandlers/` → `internal/openchoreo-api/legacymcphandlers/` (19 files)

### Deleted from `pkg/mcp/tools/`

- `build.go`, `deployment.go`, `schema.go`, `resource.go`
- `build_specs_test.go`, `deployment_specs_test.go`, `schema_specs_test.go`, `resource_specs_test.go`

### Modified in `pkg/mcp/tools/`

- `types.go` — Removed 4 toolset constants, 4 handler interfaces, trimmed ComponentToolsetHandler (24→15 methods) and InfrastructureToolsetHandler (17→13 methods)
- `register.go` — Removed 4 registration functions, trimmed component and infrastructure registrations
- `component.go` — Removed 10 tool registration functions
- `infrastructure.go` — Removed 4 tool registration functions
- `mock_test.go` — Updated mock to match trimmed interfaces
- `component_specs_test.go` — Removed 10 test specs
- `infrastructure_specs_test.go` — Removed 4 test specs
- `registration_test.go` — Removed references to deleted toolsets

### Modified in `internal/openchoreo-api/`

- `handlers/handlers.go` — Updated `getMCPServerToolsets()` to use `NewMCPHandler(h.newServices)` with 4 toolsets only
- `config/mcp.go` — Reduced defaults and valid toolsets from 8 to 4
- `config/mcp_test.go` — Updated test expectations to match 4 toolsets

---

## Verification

| Check | Result |
|-------|--------|
| `go build ./cmd/openchoreo-api/...` | Pass |
| `go vet ./internal/openchoreo-api/... ./pkg/mcp/...` | Pass |
| `go test ./pkg/mcp/tools/...` | Pass (44 tools) |
| `go test ./internal/openchoreo-api/config/...` | Pass |
| `make lint` | Pass (0 issues) |
