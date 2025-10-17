package choreomcp

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type GetOrganizationToolHandler interface {
	GetOrganization(name string) (string, error)
}

type Tools struct {
	GetOrganization GetOrganizationToolHandler
}

// Helper functions to create JSON Schema definitions
func stringProperty(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

func enumProperty(description string, values []string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
		"enum":        values,
	}
}

func createSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func (t *Tools) Register(s *mcp.Server) {
	if t.GetOrganization != nil {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "get_organization",
			Description: "Get information about organizations. If no name is provided, lists all organizations.",
			InputSchema: createSchema(map[string]any{
				"name":          stringProperty("Optional: specific organization name to retrieve"),
				"output_format": enumProperty("Output format for the results", []string{"json", "table"}),
			}, []string{}),
		}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
			Name         string `json:"name"`
			OutputFormat string `json:"output_format"`
		}) (*mcp.CallToolResult, map[string]string, error) {
			msg, err := t.GetOrganization.GetOrganization(args.Name)
			if err != nil {
				return nil, nil, err
			}
			contentBytes, err := json.Marshal(msg)
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: string(contentBytes)},
				},
			}, map[string]string{"message": string(contentBytes)}, nil
		})
	}
}
