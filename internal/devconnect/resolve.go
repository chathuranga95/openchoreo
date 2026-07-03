// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package devconnect

import (
	"maps"
	"strconv"
	"strings"
)

// ResolveRequest is what occ sends to the control plane's dev-connect resolve
// endpoint. It is built from the local workload.yaml (worklog D11): the consuming
// component's identity plus its declared dependencies, resolved for one environment.
type ResolveRequest struct {
	// Namespace is the control-plane namespace (org) the component lives in.
	Namespace   string        `json:"namespace"`
	Project     string        `json:"project"`
	Component   string        `json:"component"`
	Environment string        `json:"environment"`
	Endpoints   []EndpointDep `json:"endpoints,omitempty"`
	Resources   []ResourceDep `json:"resources,omitempty"`
}

// EndpointDep mirrors a workload endpoint dependency (spec.dependencies.endpoints[]).
type EndpointDep struct {
	Project     string              `json:"project,omitempty"`
	Component   string              `json:"component"`
	Name        string              `json:"name"`
	Visibility  string              `json:"visibility"`
	EnvBindings EndpointEnvBindings `json:"envBindings"`
}

// EndpointEnvBindings mirrors ConnectionEnvBindings: the env var NAMES the app expects
// for each resolved address component.
type EndpointEnvBindings struct {
	Address  string `json:"address,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     string `json:"port,omitempty"`
	BasePath string `json:"basePath,omitempty"`
}

// ResourceDep mirrors a workload resource dependency (spec.dependencies.resources[]).
type ResourceDep struct {
	Ref         string            `json:"ref"`
	EnvBindings map[string]string `json:"envBindings,omitempty"` // outputName -> env var name
}

// ResolveResponse is the control plane's reply: how occ renders local env + connects,
// plus the CP-signed capability the agent consumes.
type ResolveResponse struct {
	Agent         AgentEndpoint    `json:"agent"`
	Capability    string           `json:"capability"`
	Targets       []ResolvedTarget `json:"targets"`
	Unconnectable []Unconnectable  `json:"unconnectable,omitempty"`
}

// AgentEndpoint tells occ where and how to reach the dev-tunnel agent.
type AgentEndpoint struct {
	Endpoint string `json:"endpoint"`           // host:port
	CABundle string `json:"caBundle,omitempty"` // PEM; empty = system roots
}

// ResolvedTarget is one tunnellable dependency. Key matches the capability target key
// occ passes to OpenStream; the concrete host:port lives (signed) in the capability.
type ResolvedTarget struct {
	Key      string          `json:"key"`
	Proto    string          `json:"proto"` // "tcp"
	Endpoint *EndpointRender `json:"endpoint,omitempty"`
	Resource *ResourceRender `json:"resource,omitempty"`
}

// EndpointRender carries what occ needs to render endpoint env against a local port.
type EndpointRender struct {
	Scheme   string              `json:"scheme"`
	BasePath string              `json:"basePath,omitempty"`
	Bindings EndpointEnvBindings `json:"bindings"`
}

// ResourceRender carries what occ needs to render resource env against a local port.
// HostEnv/PortEnv name the env vars set to the local host/port; StaticEnv holds the
// resolved non-secret values verbatim. OmittedSecretEnv lists secret-backed env vars
// skipped in v1 (for user messaging).
type ResourceRender struct {
	HostEnv          string            `json:"hostEnv,omitempty"`
	PortEnv          string            `json:"portEnv,omitempty"`
	StaticEnv        map[string]string `json:"staticEnv,omitempty"`
	OmittedSecretEnv []string          `json:"omittedSecretEnv,omitempty"`
}

// Unconnectable reports a declared dependency that cannot be tunnelled in v1 (e.g. a
// resource whose endpoint is embedded in a composite secret output — see worklog D12).
type Unconnectable struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// RenderEnv produces the environment variables for a target, pointing the app at the
// local listener (localHost:localPort) instead of the real dependency address.
func RenderEnv(t ResolvedTarget, localHost string, localPort int) map[string]string {
	out := map[string]string{}
	lp := strconv.Itoa(localPort)
	switch {
	case t.Endpoint != nil:
		b := t.Endpoint.Bindings
		if b.Host != "" {
			out[b.Host] = localHost
		}
		if b.Port != "" {
			out[b.Port] = lp
		}
		if b.BasePath != "" {
			out[b.BasePath] = t.Endpoint.BasePath
		}
		if b.Address != "" {
			out[b.Address] = ComposeAddress(t.Endpoint.Scheme, localHost, localPort, t.Endpoint.BasePath)
		}
	case t.Resource != nil:
		maps.Copy(out, t.Resource.StaticEnv)
		if t.Resource.HostEnv != "" {
			out[t.Resource.HostEnv] = localHost
		}
		if t.Resource.PortEnv != "" {
			out[t.Resource.PortEnv] = lp
		}
	}
	return out
}

// ComposeAddress builds the connection string for an endpoint's `address` binding,
// mirroring the data plane's formatEndpointAddress: a scheme:// prefix only for
// http/https/ws/wss/tls, then host, then :port (when non-zero), then basePath (with a
// leading slash ensured) — for every scheme. Keeping this identical to the controller
// means the local `address` env var has the same shape the app sees in the cluster.
func ComposeAddress(scheme, host string, port int, basePath string) string {
	var sb strings.Builder
	if schemeUsesURLFormat(scheme) {
		sb.WriteString(scheme)
		sb.WriteString("://")
	}
	sb.WriteString(host)
	if port != 0 {
		sb.WriteString(":")
		sb.WriteString(strconv.Itoa(port))
	}
	if basePath != "" {
		if !strings.HasPrefix(basePath, "/") {
			sb.WriteString("/")
		}
		sb.WriteString(basePath)
	}
	return sb.String()
}

func schemeUsesURLFormat(scheme string) bool {
	switch scheme {
	case "http", "https", "ws", "wss", "tls":
		return true
	default:
		return false
	}
}
