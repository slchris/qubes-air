package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
)

// auditEngine builds an engine with a subject seeded (as ScopedAuth would) and
// the audit middleware attached, recording into buf.
func auditEngine(buf *bytes.Buffer, subject string, status int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if subject != "" {
			c.Set(SubjectContextKey, subject)
		}
		c.Next()
	})
	r.Use(Audit(audit.NewRecorder(buf)))
	r.POST("/qubes/:id/purge", func(c *gin.Context) { c.Status(status) })
	r.GET("/qubes", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestAuditRecordsMutatingRequest(t *testing.T) {
	var buf bytes.Buffer
	r := auditEngine(&buf, "operator@zone", http.StatusOK)

	req := httptest.NewRequest(http.MethodPost, "/qubes/qa-smoke1/purge", strings.NewReader(`{"confirm":"qa-smoke1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.9:4444"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got); err != nil {
		t.Fatalf("no JSON audit line: %v (%q)", err, buf.String())
	}
	for key, want := range map[string]any{
		"subject": "operator@zone",
		"source":  "10.0.0.9",
		"method":  "POST",
		"route":   "/qubes/:id/purge",
		"object":  "qa-smoke1",
		"outcome": audit.OutcomeSuccess,
	} {
		if got[key] != want {
			t.Errorf("audit field %q = %v, want %v", key, got[key], want)
		}
	}
	// The body must never be echoed: it can hold a credential or confirmation
	// secret, and an audit log is the last place it should appear.
	if strings.Contains(buf.String(), "confirm") {
		t.Errorf("audit line leaked the request body: %s", buf.String())
	}
}

func TestAuditSkipsReads(t *testing.T) {
	var buf bytes.Buffer
	r := auditEngine(&buf, "operator@zone", http.StatusOK)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/qubes", nil))

	if buf.Len() != 0 {
		t.Errorf("reads must not be audited, got %q", buf.String())
	}
	if got := w.Header().Get(RequestIDHeader); got != "" {
		t.Errorf("an unaudited read must not carry a request ID that matches no line, got %q", got)
	}
}

func TestAuditMarksDeniedOnUnauthorized(t *testing.T) {
	var buf bytes.Buffer
	r := auditEngine(&buf, "", http.StatusUnauthorized)

	req := httptest.NewRequest(http.MethodPost, "/qubes/qa-smoke1/purge", nil)
	req.RemoteAddr = "10.0.0.9:4444"
	r.ServeHTTP(httptest.NewRecorder(), req)

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got); err != nil {
		t.Fatalf("no JSON audit line: %v (%q)", err, buf.String())
	}
	if got["outcome"] != audit.OutcomeDenied {
		t.Errorf("outcome = %v, want denied", got["outcome"])
	}
	if got["subject"] != audit.AnonymousSubject || got["authenticated"] != false {
		t.Errorf("unauthenticated request must be recorded as anonymous, got subject=%v authenticated=%v",
			got["subject"], got["authenticated"])
	}
	if got["zone_scope"] != "none" {
		t.Errorf("unauthenticated request must not read as fleet-wide, got zone_scope=%v", got["zone_scope"])
	}
}

// TestAuditRequestIDIsServerGenerated pins that the request ID in the audit
// line is the console's own, returned to the caller, unique per request, and
// never the value a client sent: a caller that chose it could make its line
// collide with, or impersonate, another request's.
func TestAuditRequestIDIsServerGenerated(t *testing.T) {
	var buf bytes.Buffer
	r := auditEngine(&buf, "operator@zone", http.StatusOK)

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/qubes/qa-smoke1/purge", nil)
		req.Header.Set(RequestIDHeader, "client-chosen-id")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		header := w.Header().Get(RequestIDHeader)
		if header == "" || header == "client-chosen-id" {
			t.Fatalf("response request ID = %q, want a server-generated value", header)
		}
		seen[header] = true
	}
	if len(seen) != 2 {
		t.Errorf("two requests must get two request IDs, got %v", seen)
	}

	lines := auditLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("want one audit line per request, got %d: %q", len(lines), buf.String())
	}
	for _, line := range lines {
		id, _ := line["request_id"].(string)
		if !seen[id] {
			t.Errorf("audit request_id %q does not match any returned %s header", id, RequestIDHeader)
		}
	}
	if strings.Contains(buf.String(), "client-chosen-id") {
		t.Errorf("client-supplied request ID reached the audit trail: %s", buf.String())
	}
}

// TestAuditRecordsMarkedRefusalAsDenied covers a refusal whose status is not
// 401/403: RequireZones answers a foreign object with 404 so the caller learns
// nothing, but the trail must still say the request was denied.
func TestAuditRecordsMarkedRefusalAsDenied(t *testing.T) {
	var buf bytes.Buffer
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Audit(audit.NewRecorder(&buf)))
	r.POST("/qubes/:id/start", notFoundObject)
	r.POST("/qubes/:id/stop", func(c *gin.Context) { c.Status(http.StatusNotFound) })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/qubes/q2/start", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/qubes/missing/stop", nil))

	lines := auditLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("want 2 audit lines, got %d: %q", len(lines), buf.String())
	}
	if lines[0]["status"] != float64(http.StatusNotFound) || lines[0]["outcome"] != audit.OutcomeDenied {
		t.Errorf("zone refusal = status %v outcome %v, want 404 denied", lines[0]["status"], lines[0]["outcome"])
	}
	// A handler's own 404 is not an authorization decision and must not be
	// promoted to one.
	if lines[1]["outcome"] != audit.OutcomeClientError {
		t.Errorf("handler 404 outcome = %v, want client_error", lines[1]["outcome"])
	}
}

// auditLines decodes every JSON line the recorder wrote.
func auditLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("audit output is not JSON lines: %v (%q)", err, raw)
		}
		lines = append(lines, line)
	}
	return lines
}
