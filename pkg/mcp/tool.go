package mcp

import (
	"strings"

	toolpkg "github.com/kitporath/project_valhalla/pkg/tools"
)

type MCPTool struct {
	client      *Client
	toolName    string
	description string
	params      map[string]string
}

func NewMCPTool(client *Client, def MCPToolDef) *MCPTool {
	params := map[string]string{}
	if props, ok := def.InputSchema["properties"].(map[string]interface{}); ok {
		for name, propRaw := range props {
			prop, ok := propRaw.(map[string]interface{})
			if !ok {
				params[name] = "parameter"
				continue
			}
			desc := ""
			if d, ok := prop["description"].(string); ok {
				desc = d
			}
			if t, ok := prop["type"].(string); ok && desc == "" {
				desc = t + " parameter"
			}
			if desc == "" {
				desc = "parameter"
			}
			params[name] = desc
		}
	}
	return &MCPTool{
		client:      client,
		toolName:    def.Name,
		description: def.Description,
		params:      params,
	}
}

func (t *MCPTool) Name() string {
	return t.toolName
}

func (t *MCPTool) Description() string {
	return t.description
}

func (t *MCPTool) Parameters() map[string]string {
	return t.params
}

func (t *MCPTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	output, err := t.client.CallTool(t.toolName, args)
	if err != nil {
		return toolpkg.ToolResult{Error: err.Error()}
	}
	return toolpkg.ToolResult{Output: strings.TrimSpace(output)}
}
