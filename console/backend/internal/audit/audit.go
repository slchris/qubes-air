// Package audit records who did what, from where, and how it turned out.
//
// The console had a log line that named the authenticated subject and the HTTP
// method/path, but nothing machine-readable and no source, object or outcome.
// An operator answering "who purged this qube, and did it succeed?" had to
// correlate free-text lines by hand. Entries here are one JSON object per
// mutating request so that question is answerable without parsing prose.
package audit

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// Entry is one audited operator action.
//
// It deliberately carries no request body: mutating bodies can contain secrets
// (a credential value, an API token in the login exchange), and an audit log is
// the last place they should end up. Subject, source, route and object are
// enough to identify the action; the resource itself is not echoed.
type Entry struct {
	Subject   string
	Source    string
	Method    string
	Route     string
	Object    string
	Status    int
	Outcome   string
	LatencyMS int64
}

// Recorder emits entries as JSON lines.
type Recorder struct {
	logger *slog.Logger
}

// NewRecorder builds a Recorder writing JSON lines to w.
func NewRecorder(w io.Writer) *Recorder {
	return &Recorder{
		logger: slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
}

// Record writes one entry. The handler stamps the time, so it is not carried on
// Entry (two "time" keys would be worse than none).
func (r *Recorder) Record(e Entry) {
	r.logger.LogAttrs(context.Background(), slog.LevelInfo, "audit",
		slog.String("subject", e.Subject),
		slog.String("source", e.Source),
		slog.String("method", e.Method),
		slog.String("route", e.Route),
		slog.String("object", e.Object),
		slog.Int("status", e.Status),
		slog.String("outcome", e.Outcome),
		slog.Int64("latency_ms", e.LatencyMS),
	)
}

// Outcome values. They are exported because callers and tests switch on them;
// a typo in a string literal here would silently stop matching.
const (
	OutcomeSuccess     = "success"
	OutcomeDenied      = "denied"
	OutcomeClientError = "client_error"
	OutcomeError       = "error"
)

// Outcome classifies an HTTP status for the audit trail: OutcomeDenied
// specifically marks an authorization failure, which is the line an operator
// greps for.
func Outcome(status int) string {
	switch {
	case status >= 200 && status < 300:
		return OutcomeSuccess
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return OutcomeDenied
	case status >= 400 && status < 500:
		return OutcomeClientError
	default:
		return OutcomeError
	}
}

// Object extracts the resource identifier from the request's path parameters.
// "id" is the convention across the API; "app" is the one nested launch route.
func Object(params func(string) string) string {
	if id := strings.TrimSpace(params("id")); id != "" {
		return id
	}
	return strings.TrimSpace(params("app"))
}
