// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package mcphandlers

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// wrapList wraps a slice in a map so that the MCP structured content response
// is a JSON object (record) instead of a bare array. The MCP specification
// requires structuredContent to be a record; returning an array directly
// causes validation errors.
func wrapList(key string, items any) map[string]any {
	return map[string]any{key: items}
}
