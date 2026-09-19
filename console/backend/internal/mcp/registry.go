package mcp

import (
	"context"
)

// Scope is the privilege level an MCP process runs at. It is enforced in two
// directions: tools of a higher scope do not appear in tools/list, and a direct
// tools/call of them is refused even when the caller knows their name.
type Scope string

const (
	// ScopeReadOnly exposes list/detail GET endpoints only.
	ScopeReadOnly Scope = "read-only"
	// ScopeControl additionally exposes the write endpoints that exist on the
	// Console API (create / start / stop / delete / acknowledge).
	ScopeControl Scope = "control"
)

// ParseScope maps a --scope flag value to a Scope.
func ParseScope(s string) (Scope, bool) {
	switch Scope(s) {
	case ScopeReadOnly:
		return ScopeReadOnly, true
	case ScopeControl:
		return ScopeControl, true
	}
	return "", false
}

// ToolHandler executes a tool. A non-nil error is a protocol-level violation
// (missing or invalid required argument); operational failures (upstream
// 4xx/5xx, timeout, not implemented) are returned inside *CallToolResult with
// IsError set.
type ToolHandler func(ctx context.Context, args map[string]any) (*CallToolResult, error)

// Tool is one registered MCP tool.
//
// Method, Path, PathArg and Scope are internal machinery and carry no JSON
// tags: they exist so tests can assert the tool → Console endpoint mapping line
// by line (See AGENTS.md: create nothing not backed by a real route). Path uses
// {id} as the placeholder for a path argument named by PathArg; the value is
// allowlisted before substitution.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Scope       Scope
	Method      string
	Path        string
	PathArg     string
	Handler     ToolHandler
}

// ToolInfo returns the part of a Tool that tools/list may publish.
func (t *Tool) ToolInfo() ToolInfo {
	return ToolInfo{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: t.InputSchema,
	}
}

// Registry is the set of tools visible at one MCP scope plus whether the
// computer-use group is enabled. Everything else in the process derives from
// it, so a read-only Registry provably has no control tool to hand out.
type Registry struct {
	scope   Scope
	byName  map[string]*Tool
	ordered []*Tool
}

// NewRegistry assembles the tools for a scope. enableComputerUse only matters
// under control scope: computer-use tools are actions, so a read-only process
// never exposes them even when the flag is set.
func NewRegistry(scope Scope, enableComputerUse bool, cl *Client) *Registry {
	r := &Registry{
		scope:  scope,
		byName: make(map[string]*Tool),
	}
	for _, t := range readTools(cl) {
		r.add(t)
	}
	if scope == ScopeControl {
		for _, t := range controlTools(cl) {
			r.add(t)
		}
		if enableComputerUse {
			for _, t := range computerUseTools(cl) {
				r.add(t)
			}
		}
	}
	return r
}

func (r *Registry) add(t *Tool) {
	if _, exists := r.byName[t.Name]; exists {
		panic("mcp: duplicate tool name " + t.Name)
	}
	r.byName[t.Name] = t
	r.ordered = append(r.ordered, t)
}

// Scope reports the registry's scope.
func (r *Registry) Scope() Scope { return r.scope }

// Tool looks up a tool by name.
func (r *Registry) Tool(name string) (*Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Tools returns the public tools/list view in registration order.
func (r *Registry) Tools() []ToolInfo {
	out := make([]ToolInfo, 0, len(r.ordered))
	for _, t := range r.ordered {
		out = append(out, t.ToolInfo())
	}
	return out
}
