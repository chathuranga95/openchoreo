// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package depconnect

import "testing"

func TestRenderEnvEndpoint(t *testing.T) {
	tgt := ResolvedTarget{
		Key:   "ep/backend-api/http",
		Proto: "tcp",
		Endpoint: &EndpointRender{
			Scheme:   "http",
			BasePath: "/api",
			Bindings: EndpointEnvBindings{Address: "BACKEND_API_URL", Host: "BACKEND_HOST", Port: "BACKEND_PORT", BasePath: "BACKEND_PATH"},
		},
	}
	env := RenderEnv(tgt, "127.0.0.1", 9090)
	if got := env["BACKEND_API_URL"]; got != "http://127.0.0.1:9090/api" {
		t.Errorf("address = %q", got)
	}
	if env["BACKEND_HOST"] != "127.0.0.1" || env["BACKEND_PORT"] != "9090" || env["BACKEND_PATH"] != "/api" {
		t.Errorf("component bindings wrong: %+v", env)
	}
}

func TestRenderEnvResource(t *testing.T) {
	tgt := ResolvedTarget{
		Key:   "res/doclet-postgres",
		Proto: "tcp",
		Resource: &ResourceRender{
			HostEnv:   "DB_HOST",
			PortEnv:   "DB_PORT",
			StaticEnv: map[string]string{"DB_NAME": "doclet", "DB_USER": "doclet-user"},
		},
	}
	env := RenderEnv(tgt, "127.0.0.1", 55432)
	if env["DB_HOST"] != "127.0.0.1" || env["DB_PORT"] != "55432" {
		t.Errorf("host/port rewrite wrong: %+v", env)
	}
	if env["DB_NAME"] != "doclet" || env["DB_USER"] != "doclet-user" {
		t.Errorf("static env not passed through: %+v", env)
	}
}

func TestComposeAddress(t *testing.T) {
	cases := []struct {
		scheme, basePath, want string
	}{
		{"http", "/api", "http://127.0.0.1:8080/api"},
		{"http", "api", "http://127.0.0.1:8080/api"}, // leading slash ensured
		{"https", "", "https://127.0.0.1:8080"},
		{"grpc", "", "127.0.0.1:8080"},    // no scheme:// prefix
		{"tcp", "/x", "127.0.0.1:8080/x"}, // path appended for all schemes (matches DP)
	}
	for _, c := range cases {
		if got := ComposeAddress(c.scheme, "127.0.0.1", 8080, c.basePath); got != c.want {
			t.Errorf("ComposeAddress(%q,%q) = %q, want %q", c.scheme, c.basePath, got, c.want)
		}
	}
}
