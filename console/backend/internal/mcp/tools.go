package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidParams marks a tools/call whose arguments are missing or fail the
// allowlist. The serve loop answers it with JSON-RPC -32602.
var ErrInvalidParams = errors.New("invalid tool parameters")

// safePathID is the allowlist every argument substituted into a URL path
// segment must match. The console mints UUIDs, but the guard is deliberately
// about shape, not format: no path separators and no length explosion, so a
// crafted id cannot drag a request onto a route it was not meant for.
var safePathID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// strProp is one string property of an input schema.
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// objSchema builds a JSON object schema (MCP inputSchema must be an object).
func objSchema(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// queryString reads an optional string argument returned as empty when absent
// or of the wrong type.
func queryString(args map[string]any, key string) string {
	v, ok := args[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// pathID reads a required path argument and allowlists it for use in a URL
// path segment.
func pathID(args map[string]any, key string) (string, error) {
	id := queryString(args, key)
	if id == "" || !safePathID.MatchString(id) {
		return "", ErrInvalidParams
	}
	return id, nil
}

// textBlocks renders one or more text content items for a CallToolResult.
func textBlocks(ts ...string) []ContentItem {
	out := make([]ContentItem, 0, len(ts))
	for _, t := range ts {
		out = append(out, ContentItem{Type: "text", Text: t})
	}
	return out
}

// resultError builds a failed tool result. Operational failures (upstream
// errors, timeouts, not-implemented stubs) are reported through the result,
// never as JSON-RPC errors, so the MCP client sees them with IsError set.
func resultError(format string, args ...any) *CallToolResult {
	return &CallToolResult{IsError: true, Content: textBlocks(fmt.Sprintf(format, args...))}
}

// doRequest performs one Console API call and maps the outcome onto a tool
// result. Non-2xx statuses are passed through as failed results carrying the
// upstream status and message; transport failures (timeout, oversized body)
// surface as failed results too. The bearer token never reaches this layer.
func doRequest(cl *Client, ctx context.Context, method, path string, query url.Values, body any) (*CallToolResult, error) {
	resp, err := cl.Do(ctx, method, path, query, body)
	if err != nil {
		return resultError("console API request failed: %v", err), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		payload := strings.TrimSpace(string(resp.Body))
		if payload == "" {
			return resultError("upstream returned HTTP %d", resp.StatusCode), nil
		}
		return resultError("upstream returned HTTP %d: %s", resp.StatusCode, payload), nil
	}
	return &CallToolResult{Content: textBlocks(string(resp.Body))}, nil
}

// callPath resolves the tool's {id} placeholder from its arguments (allowlisted)
// and fires the request. It is the handler behind every read and simple
// control tool.
func callPath(cl *Client, ctx context.Context, t *Tool, args map[string]any, query url.Values, body any) (*CallToolResult, error) {
	path := t.Path
	if t.PathArg != "" {
		id, err := pathID(args, t.PathArg)
		if err != nil {
			return nil, err
		}
		path = strings.ReplaceAll(path, "{id}", id)
	}
	return doRequest(cl, ctx, t.Method, path, query, body)
}

// queryFromArgs collects the optional query parameters a tool declares. Tool
// argument names equal the query parameter names of the Console API.
func queryFromArgs(args map[string]any, props map[string]any) url.Values {
	q := url.Values{}
	for key := range props {
		if v := queryString(args, key); v != "" {
			q.Set(key, v)
		}
	}
	return q
}

// readGET declares a read-only GET tool against a real Console API route. The
// tool's optional arguments become query parameters of the same name.
func readGET(cl *Client, name, desc, path, pathArg string, props map[string]any, required ...string) *Tool {
	t := &Tool{
		Name:        name,
		Description: desc,
		InputSchema: objSchema(props, required...),
		Scope:       ScopeReadOnly,
		Method:      http.MethodGet,
		Path:        path,
		PathArg:     pathArg,
	}
	t.Handler = func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		return callPath(cl, ctx, t, args, queryFromArgs(args, props), nil)
	}
	return t
}

// writeAction declares a control tool that posts an empty action against a
// real write route (start / stop / delete / acknowledge).
func writeAction(cl *Client, name, desc, method, path, pathArg string) *Tool {
	t := &Tool{
		Name:        name,
		Description: desc,
		InputSchema: objSchema(map[string]any{
			"id": strProp("The resource id to act on (UUID)."),
		}, "id"),
		Scope:   ScopeControl,
		Method:  method,
		Path:    path,
		PathArg: pathArg,
	}
	t.Handler = func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		return callPath(cl, ctx, t, args, nil, nil)
	}
	return t
}

// bodyTool is a control tool that relays its arguments verbatim as the request
// body (MCP does not re-implement the API's own validation; see §2 of the
// design doc).
func bodyTool(cl *Client, name, desc, method, path, pathArg string, schema map[string]any, required ...string) *Tool {
	t := &Tool{
		Name:        name,
		Description: desc,
		InputSchema: objSchema(schema, required...),
		Scope:       ScopeControl,
		Method:      method,
		Path:        path,
		PathArg:     pathArg,
	}
	t.Handler = func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		return callPath(cl, ctx, t, args, nil, args)
	}
	return t
}

// readTools lists the read-only tools, every one backed by a GET endpoint that
// exists in console/backend/internal/handler (qube, zone, infrastructure, job,
// monitoring, settings, credentials-metadata) plus /api/v1/status.
func readTools(cl *Client) []*Tool {
	return []*Tool{
		readGET(cl, "qube_list", "List qubes (GET /api/v1/qubes). Optional filters: zone_id, status, type.",
			"/api/v1/qubes", "",
			map[string]any{"zone_id": strProp("Filter by zone id."), "status": strProp("Filter by qube status."), "type": strProp("Filter by qube type.")}),
		readGET(cl, "qube_get", "Get one qube (GET /api/v1/qubes/{id}).",
			"/api/v1/qubes/{id}", "id",
			nil, "id"),
		readGET(cl, "qube_check_reachable", "Probe a qube's agent over the network transport (GET /api/v1/qubes/{id}/reachable). 502 when unreachable.",
			"/api/v1/qubes/{id}/reachable", "id",
			nil, "id"),
		readGET(cl, "qube_list_certs", "List the certificates issued to a qube's agent — metadata only, never key material (GET /api/v1/qubes/{id}/certs).",
			"/api/v1/qubes/{id}/certs", "id",
			nil, "id"),
		readGET(cl, "zone_list", "List zones (GET /api/v1/zones). Optional filters: status, type.",
			"/api/v1/zones", "",
			map[string]any{"status": strProp("Filter by zone status."), "type": strProp("Filter by zone type.")}),
		readGET(cl, "zone_get", "Get one zone (GET /api/v1/zones/{id}).",
			"/api/v1/zones/{id}", "id",
			nil, "id"),
		readGET(cl, "zone_capacity", "Report a zone's free capacity (GET /api/v1/zones/{id}/capacity).",
			"/api/v1/zones/{id}/capacity", "id",
			nil, "id"),
		readGET(cl, "infrastructure_list", "List infrastructure providers (GET /api/v1/infrastructure).",
			"/api/v1/infrastructure", "", nil),
		readGET(cl, "infrastructure_get", "Get one infrastructure provider, including connection/credential metadata (GET /api/v1/infrastructure/{id}).",
			"/api/v1/infrastructure/{id}", "id",
			nil, "id"),
		readGET(cl, "job_list", "List orchestration jobs (GET /api/v1/jobs). Optional: limit, qube_id.",
			"/api/v1/jobs", "",
			map[string]any{"limit": strProp("Max jobs (default 100, capped 500)."), "qube_id": strProp("Filter to one qube.")}),
		readGET(cl, "job_get", "Get one orchestration job (GET /api/v1/jobs/{id}).",
			"/api/v1/jobs/{id}", "id",
			nil, "id"),
		readGET(cl, "job_log", "Read a job's terraform output (GET /api/v1/jobs/{id}/log). Optional offset for incremental polling.",
			"/api/v1/jobs/{id}/log", "id",
			map[string]any{"offset": strProp("Byte offset to read from (for polling).")}, "id"),
		readGET(cl, "credential_list", "List stored credentials — metadata only, secret values are never returned (GET /api/v1/credentials).",
			"/api/v1/credentials", "", nil),
		readGET(cl, "credential_get", "Get one stored credential — metadata only (GET /api/v1/credentials/{id}).",
			"/api/v1/credentials/{id}", "id",
			nil, "id"),
		readGET(cl, "monitoring_overview", "Console process monitoring overview (GET /api/v1/monitoring).",
			"/api/v1/monitoring", "", nil),
		readGET(cl, "monitoring_metrics", "Console process metrics (GET /api/v1/monitoring/metrics).",
			"/api/v1/monitoring/metrics", "", nil),
		readGET(cl, "monitoring_alerts", "List monitoring alerts (GET /api/v1/monitoring/alerts).",
			"/api/v1/monitoring/alerts", "", nil),
		readGET(cl, "settings_get", "Read console settings (GET /api/v1/settings).",
			"/api/v1/settings", "", nil),
		readGET(cl, "status", "Console version and name (GET /api/v1/status).",
			"/api/v1/status", "", nil),
	}
}

// qubeCreateSchema mirrors models.QubeCreateRequest.
var qubeCreateSchema = map[string]any{
	"name":    strProp("Qube name (required)."),
	"zone_id": strProp("Zone the qube belongs to (optional)."),
	"type":    strProp("Qube type: app, work, dev, gpu, disp, sys (required)."),
	"spec": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"vcpu":         map[string]any{"type": "integer", "description": "vCPUs."},
			"memory":       map[string]any{"type": "integer", "description": "Memory in MB."},
			"disk":         map[string]any{"type": "integer", "description": "Root disk in GB."},
			"data_disk_gb": map[string]any{"type": "integer", "description": "Persistent data disk in GB."},
			"node":         strProp("Cluster node to pin the qube to."),
			"encrypt_data": map[string]any{"type": "boolean", "description": "Encrypt the data disk (nil = fleet default)."},
		},
	},
}

// controlTools lists the control tools. Only scopes that exist in the handler
// routes are offered; the design's suspend/resume example is deliberately
// absent because no such endpoint is registered.
func controlTools(cl *Client) []*Tool {
	return []*Tool{
		bodyTool(cl, "qube_create", "Create a qube (POST /api/v1/qubes). Queues a provision job; poll job_list/job_get for the outcome.",
			http.MethodPost, "/api/v1/qubes", "", qubeCreateSchema, "name", "type"),
		bodyTool(cl, "qube_update", "Update a qube (PUT /api/v1/qubes/{id}). Body: name and/or spec.",
			http.MethodPut, "/api/v1/qubes/{id}", "id",
			map[string]any{"name": strProp("New name."), "spec": qubeCreateSchema["spec"]}, "id"),
		writeAction(cl, "qube_delete", "Release a qube: compute is destroyed, the data disk is retained (DELETE /api/v1/qubes/{id}).",
			http.MethodDelete, "/api/v1/qubes/{id}", "id"),
		writeAction(cl, "qube_start", "Start (resume) a qube (POST /api/v1/qubes/{id}/start). Queues a job.",
			http.MethodPost, "/api/v1/qubes/{id}/start", "id"),
		writeAction(cl, "qube_stop", "Stop (suspend) a qube (POST /api/v1/qubes/{id}/stop). Queues a job.",
			http.MethodPost, "/api/v1/qubes/{id}/stop", "id"),
		writeAction(cl, "alert_acknowledge", "Acknowledge a monitoring alert (POST /api/v1/monitoring/alerts/{id}/acknowledge).",
			http.MethodPost, "/api/v1/monitoring/alerts/{id}/acknowledge", "id"),
	}
}

// computerUseTools are Phase-1 stubs. They stay unregistered unless
// --enable-computer-use is set, and even then every call fails loudly: the
// group would act as the user on a graphical desktop, and nothing like that is
// implemented yet. Better an explicit refusal than a silent no-op.
func computerUseTools() []*Tool {
	return []*Tool{
		{
			Name:        "desktop_apps_list",
			Description: "(not implemented) List applications available in the connected desktop session.",
			InputSchema: objSchema(nil),
			Scope:       ScopeControl,
			Method:      "N/A",
			Path:        "",
			Handler: func(context.Context, map[string]any) (*CallToolResult, error) {
				return resultError("computer-use is not implemented in this phase; the desktop tool group is a stub"), nil
			},
		},
		{
			Name:        "desktop_app_launch",
			Description: "(not implemented) Launch an application in the desktop session.",
			InputSchema: objSchema(map[string]any{"app": strProp("Application name.")}, "app"),
			Scope:       ScopeControl,
			Method:      "N/A",
			Path:        "",
			Handler: func(context.Context, map[string]any) (*CallToolResult, error) {
				return resultError("computer-use is not implemented in this phase; the desktop tool group is a stub"), nil
			},
		},
		{
			Name:        "desktop_frame_get",
			Description: "(not implemented) Capture one frame of the desktop session.",
			InputSchema: objSchema(nil),
			Scope:       ScopeControl,
			Method:      "N/A",
			Path:        "",
			Handler: func(context.Context, map[string]any) (*CallToolResult, error) {
				return resultError("computer-use is not implemented in this phase; the desktop tool group is a stub"), nil
			},
		},
		{
			Name:        "desktop_input_send",
			Description: "(not implemented) Inject input events into the desktop session.",
			InputSchema: objSchema(map[string]any{"events": map[string]any{"type": "array", "description": "Input events."}}, "events"),
			Scope:       ScopeControl,
			Method:      "N/A",
			Path:        "",
			Handler: func(context.Context, map[string]any) (*CallToolResult, error) {
				return resultError("computer-use is not implemented in this phase; the desktop tool group is a stub"), nil
			},
		},
	}
}
