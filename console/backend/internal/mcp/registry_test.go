package mcp

import (
	"strings"
	"testing"
)

func TestParseScope(t *testing.T) {
	cases := []struct {
		in   string
		want Scope
		ok   bool
	}{
		{"read-only", ScopeReadOnly, true},
		{"control", ScopeControl, true},
		{"", "", false},
		{"admin", "", false},
		{"CONTROL", "", false},
	}
	for _, c := range cases {
		got, ok := ParseScope(c.in)
		if ok != c.ok || got != c.want {
			t.Fatalf("ParseScope(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func names(reg *Registry) []string {
	infos := reg.Tools()
	out := make([]string, 0, len(infos))
	for _, ti := range infos {
		out = append(out, ti.Name)
	}
	return out
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

func TestRegistry_ReadOnlyHideControlTools(t *testing.T) {
	reg := readOnlyReg(t)
	got := names(reg)

	if len(got) != 19 {
		t.Fatalf("read-only tools = %d (%v), want 19", len(got), got)
	}
	for _, control := range []string{"qube_create", "qube_start", "qube_delete", "alert_acknowledge"} {
		if contains(got, control) {
			t.Fatalf("read-only registry must not expose %q", control)
		}
	}
	for _, read := range []string{"qube_list", "qube_get", "status", "job_log", "settings_get"} {
		if !contains(got, read) {
			t.Fatalf("read-only registry missing %q", read)
		}
	}
}

func TestRegistry_ControlExposesControlTools(t *testing.T) {
	reg := controlReg(t, false)
	got := names(reg)

	if len(got) != 26 {
		t.Fatalf("control tools = %d (%v), want 26", len(got), got)
	}
	for _, control := range []string{"qube_create", "qube_update", "qube_delete", "qube_purge", "qube_start", "qube_stop", "alert_acknowledge"} {
		if !contains(got, control) {
			t.Fatalf("control registry missing %q", control)
		}
	}
	for _, cu := range []string{"desktop_apps_list", "desktop_frame_get", "desktop_input_send"} {
		if contains(got, cu) {
			t.Fatalf("computer-use tool %q must not appear without --enable-computer-use", cu)
		}
	}
}

func TestRegistry_ComputerUseOnlyWhenEnabledAndControl(t *testing.T) {
	cases := []struct {
		scope Scope
		cu    bool
		want  int
	}{
		{ScopeReadOnly, true, 19},
		{ScopeReadOnly, false, 19},
		{ScopeControl, false, 26},
		{ScopeControl, true, 30},
	}
	for _, c := range cases {
		reg := NewRegistry(c.scope, c.cu, dummyClient())
		got := names(reg)
		if len(got) != c.want {
			t.Fatalf("scope=%s cu=%v: %d tools, want %d", c.scope, c.cu, len(got), c.want)
		}
		hasCU := contains(got, "desktop_frame_get")
		wantCU := c.scope == ScopeControl && c.cu
		if hasCU != wantCU {
			t.Fatalf("scope=%s cu=%v: computer-use present=%v want %v", c.scope, c.cu, hasCU, wantCU)
		}
	}
}

// endpointMap is the ground truth read from console/backend/internal/handler:
// every tool here maps 1:1 to a registered route. Adding a tool not backed by
// an existing route fails this test on purpose.
type endpointMap struct {
	name, method, path, pathArg string
}

func expectedEndpoints() []endpointMap {
	return []endpointMap{
		{"qube_list", "GET", "/api/v1/qubes", ""},
		{"qube_get", "GET", "/api/v1/qubes/{id}", "id"},
		{"qube_check_reachable", "GET", "/api/v1/qubes/{id}/reachable", "id"},
		{"qube_list_certs", "GET", "/api/v1/qubes/{id}/certs", "id"},
		{"zone_list", "GET", "/api/v1/zones", ""},
		{"zone_get", "GET", "/api/v1/zones/{id}", "id"},
		{"zone_capacity", "GET", "/api/v1/zones/{id}/capacity", "id"},
		{"infrastructure_list", "GET", "/api/v1/infrastructure", ""},
		{"infrastructure_get", "GET", "/api/v1/infrastructure/{id}", "id"},
		{"job_list", "GET", "/api/v1/jobs", ""},
		{"job_get", "GET", "/api/v1/jobs/{id}", "id"},
		{"job_log", "GET", "/api/v1/jobs/{id}/log", "id"},
		{"credential_list", "GET", "/api/v1/credentials", ""},
		{"credential_get", "GET", "/api/v1/credentials/{id}", "id"},
		{"monitoring_overview", "GET", "/api/v1/monitoring", ""},
		{"monitoring_metrics", "GET", "/api/v1/monitoring/metrics", ""},
		{"monitoring_alerts", "GET", "/api/v1/monitoring/alerts", ""},
		{"settings_get", "GET", "/api/v1/settings", ""},
		{"status", "GET", "/api/v1/status", ""},
		{"qube_create", "POST", "/api/v1/qubes", ""},
		{"qube_update", "PUT", "/api/v1/qubes/{id}", "id"},
		{"qube_delete", "DELETE", "/api/v1/qubes/{id}", "id"},
		{"qube_start", "POST", "/api/v1/qubes/{id}/start", "id"},
		{"qube_stop", "POST", "/api/v1/qubes/{id}/stop", "id"},
		{"qube_purge", "POST", "/api/v1/qubes/{id}/purge", "id"},
		{"alert_acknowledge", "POST", "/api/v1/monitoring/alerts/{id}/acknowledge", "id"},
		{"desktop_apps_list", "GET", "/api/v1/qubes/{id}/appmenus", "id"},
		{"desktop_app_launch", "POST", "/api/v1/qubes/{id}/apps/{app}/launch", "id"},
		{"desktop_frame_get", "N/A", "", ""},
		{"desktop_input_send", "N/A", "", ""},
	}
}

func TestRegistry_EveryToolBacksARealRoute(t *testing.T) {
	reg := controlReg(t, true)
	byname := make(map[string]*Tool, 29)
	for _, name := range names(reg) {
		tool, ok := reg.Tool(name)
		if !ok {
			t.Fatalf("tool %q missing from lookup", name)
		}
		byname[name] = tool
	}

	for _, exp := range expectedEndpoints() {
		tool, ok := byname[exp.name]
		if !ok {
			t.Fatalf("expected endpoint entry has no registered tool: %s", exp.name)
		}
		if tool.Method != exp.method || tool.Path != exp.path || tool.PathArg != exp.pathArg {
			t.Errorf("%s: got %s %s (arg=%s), want %s %s (arg=%s)",
				tool.Name, tool.Method, tool.Path, tool.PathArg, exp.method, exp.path, exp.pathArg)
		}
	}
	if len(byname) != len(expectedEndpoints()) {
		t.Fatalf("registry has %d tools, endpoint map has %d", len(byname), len(expectedEndpoints()))
	}
}

func TestRegistry_ToolInfoIsSchemaSafe(t *testing.T) {
	reg := controlReg(t, true)
	for _, ti := range reg.Tools() {
		if strings.TrimSpace(ti.Name) == "" || strings.TrimSpace(ti.Description) == "" {
			t.Fatalf("tool %q missing description", ti.Name)
		}
		if _, ok := ti.InputSchema["type"]; !ok {
			t.Fatalf("tool %q inputSchema is not an object schema", ti.Name)
		}
	}
}
