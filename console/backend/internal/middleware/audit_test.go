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

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/qubes", nil))

	if buf.Len() != 0 {
		t.Errorf("reads must not be audited, got %q", buf.String())
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
	if got["subject"] != "" {
		t.Errorf("unauthenticated subject must be empty, got %v", got["subject"])
	}
}
