package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestOutcomeClassifiesAuthorizationFailures(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{200, OutcomeSuccess},
		{204, OutcomeSuccess},
		{401, OutcomeDenied},
		{403, OutcomeDenied},
		{400, OutcomeClientError},
		{429, OutcomeClientError},
		{500, OutcomeError},
		{503, OutcomeError},
	}
	for _, tc := range cases {
		if got := Outcome(tc.status); got != tc.want {
			t.Errorf("Outcome(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestObjectPrefersIDThenApp(t *testing.T) {
	params := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if got := Object(params(map[string]string{"id": "qube-1", "app": "firefox"})); got != "qube-1" {
		t.Errorf("id must win, got %q", got)
	}
	if got := Object(params(map[string]string{"app": "firefox"})); got != "firefox" {
		t.Errorf("app is the fallback, got %q", got)
	}
	if got := Object(params(nil)); got != "" {
		t.Errorf("no params means no object, got %q", got)
	}
}

func TestRecorderEmitsJSONLine(t *testing.T) {
	var buf bytes.Buffer
	NewRecorder(&buf).Record(Entry{
		RequestID:     "REQ1",
		Authenticated: true,
		Subject:       "operator@zone",
		Source:        "10.0.0.9",
		Method:        "POST",
		Route:         "/api/v1/qubes/:id/purge",
		Object:        "qa-smoke1",
		Status:        200,
		Outcome:       "success",
		LatencyMS:     12,
	})

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no audit output")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("audit output is not JSON: %v\n%s", err, line)
	}
	for key, want := range map[string]any{
		"msg":           "audit",
		"request_id":    "REQ1",
		"authenticated": true,
		"subject":       "operator@zone",
		"zone_scope":    "fleet",
		"source":        "10.0.0.9",
		"method":        "POST",
		"route":         "/api/v1/qubes/:id/purge",
		"object":        "qa-smoke1",
		"status":        float64(200),
		"outcome":       "success",
	} {
		if got[key] != want {
			t.Errorf("audit field %q = %v, want %v", key, got[key], want)
		}
	}
	if _, ok := got["time"]; !ok {
		t.Error("audit entry must be timestamped")
	}
}

// TestRecorderRendersUnauthenticatedEntry pins how a request with no resolved
// credential reads in the trail: anonymous, and with no zone scope rather than
// the fleet-wide label an authenticated credential without zones gets.
func TestRecorderRendersUnauthenticatedEntry(t *testing.T) {
	var buf bytes.Buffer
	NewRecorder(&buf).Record(Entry{
		RequestID: "REQ2",
		// Authenticated is left false on purpose while Subject and ZoneScope
		// are set: the flag wins, so an entry that forgot to set it never
		// claims an identity nobody proved.
		Subject:   "operator@zone",
		ZoneScope: []string{"zone-a"},
		Method:    "POST",
		Route:     "/api/v1/qubes/:id/purge",
		Status:    401,
		Outcome:   OutcomeDenied,
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got); err != nil {
		t.Fatalf("audit output is not JSON: %v\n%s", err, buf.String())
	}
	for key, want := range map[string]any{
		"authenticated": false,
		"subject":       AnonymousSubject,
		"zone_scope":    "none",
		"outcome":       OutcomeDenied,
	} {
		if got[key] != want {
			t.Errorf("audit field %q = %v, want %v", key, got[key], want)
		}
	}
	if strings.Contains(buf.String(), "operator@zone") || strings.Contains(buf.String(), "zone-a") {
		t.Errorf("unauthenticated entry leaked an unproven identity: %s", buf.String())
	}
}
