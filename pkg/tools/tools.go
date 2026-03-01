// Package tools provides the tool framework for Valhalla agents.
package tools

import "sort"

// ToolResult is the structured return value from a tool execution.
type ToolResult struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// Tool defines the interface that all Valhalla tools implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]string
	Execute(args map[string]interface{}) ToolResult
}

// Verifier is an optional interface for tools that can independently confirm
// their reported result after execution.
type Verifier interface {
	Verify(args map[string]interface{}, result ToolResult) error
}

// Registry stores named tools.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds or replaces a tool by name.
func (r *Registry) Register(t Tool) {
	if t == nil {
		return
	}
	r.tools[t.Name()] = t
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// VerifyResult runs post-execution verification when the tool opts in.
func (r *Registry) VerifyResult(name string, args map[string]interface{}, result ToolResult) error {
	t, ok := r.tools[name]
	if !ok {
		return nil
	}
	v, ok := t.(Verifier)
	if !ok {
		return nil
	}
	return v.Verify(args, result)
}

// List returns sorted tool names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RegisterDefaults registers exec, read, and write tools.
func (r *Registry) RegisterDefaults(workDir string) {
	r.Register(NewExecTool())
	r.Register(NewReadTool(workDir))
	r.Register(NewWriteTool(workDir))
}
