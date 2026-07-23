// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package local

import "testing"

func TestBuildDepConnectStreamURL(t *testing.T) {
	got, err := buildDepConnectStreamURL("https://cp.example.com", "default", "doclet-document", "res/doclet-postgres")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://cp.example.com/depconnect/namespaces/default/components/doclet-document?key=res%2Fdoclet-postgres"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
