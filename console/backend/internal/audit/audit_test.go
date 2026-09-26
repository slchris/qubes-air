package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
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
		"msg":              "audit",
		"request_id":       "REQ1",
		"authenticated":    true,
		"subject":          "operator@zone",
		"zone_scope":       "fleet",
		"source":           "10.0.0.9",
		"method":           "POST",
		"route":            "/api/v1/qubes/:id/purge",
		"object":           "qa-smoke1",
		"object_truncated": false,
		"status":           float64(200),
		"outcome":          "success",
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

// TestRecorderRendersAuthDisabledEntry pins the zone scope of a request made
// while authentication is disabled: it reached everything, so it must not
// read "none", and the flag must not change how an authenticated entry reads.
func TestRecorderRendersAuthDisabledEntry(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
		want  map[string]any
	}{
		{
			name:  "unauthenticated",
			entry: Entry{AuthDisabled: true, Subject: "operator@zone", ZoneScope: []string{"zone-a"}},
			want:  map[string]any{"authenticated": false, "subject": AnonymousSubject, "zone_scope": "unrestricted"},
		},
		{
			name:  "authenticated",
			entry: Entry{AuthDisabled: true, Authenticated: true, Subject: "operator@zone", ZoneScope: []string{"zone-a"}},
			want:  map[string]any{"authenticated": true, "subject": "operator@zone", "zone_scope": "zone-a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			NewRecorder(&buf).Record(tc.entry)
			got := decodeLine(t, &buf)
			for key, want := range tc.want {
				if got[key] != want {
					t.Errorf("audit field %q = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}

// TestBoundObjectCutsOnCharacterBoundary covers the object cap at and around
// MaxObjectBytes, including a multi-byte character straddling the cap, which
// must be dropped whole rather than split into invalid UTF-8.
func TestBoundObjectCutsOnCharacterBoundary(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name, in, want string
		truncated      bool
	}{
		{name: "empty", in: "", want: ""},
		{name: "at cap", in: a(MaxObjectBytes), want: a(MaxObjectBytes)},
		{name: "one over", in: a(MaxObjectBytes + 1), want: a(MaxObjectBytes), truncated: true},
		{name: "two-byte rune straddles cap", in: a(MaxObjectBytes-1) + "é", want: a(MaxObjectBytes - 1), truncated: true},
		{name: "three-byte rune straddles cap", in: a(MaxObjectBytes-2) + "€x", want: a(MaxObjectBytes - 2), truncated: true},
		{name: "rune ends at cap", in: a(MaxObjectBytes-2) + "é" + "x", want: a(MaxObjectBytes-2) + "é", truncated: true},
		{name: "invalid bytes", in: strings.Repeat("\xff", MaxObjectBytes+5), want: strings.Repeat("\xff", MaxObjectBytes), truncated: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := boundObject(tc.in)
			if got != tc.want || truncated != tc.truncated {
				t.Errorf("boundObject(%d bytes) = (%d bytes, %v), want (%d bytes, %v)",
					len(tc.in), len(got), truncated, len(tc.want), tc.truncated)
			}
			if len(got) > MaxObjectBytes {
				t.Errorf("kept %d bytes, over the %d cap", len(got), MaxObjectBytes)
			}
		})
	}
}

// TestRecorderTruncatesObject checks the cap is applied on the way out and
// flagged, so a reader can tell a cut object from a short one.
func TestRecorderTruncatesObject(t *testing.T) {
	var buf bytes.Buffer
	NewRecorder(&buf).Record(Entry{Object: strings.Repeat("q", 10*MaxObjectBytes)})

	got := decodeLine(t, &buf)
	if got["object"] != strings.Repeat("q", MaxObjectBytes) || got["object_truncated"] != true {
		t.Errorf("object = %d chars truncated=%v, want %d chars truncated=true",
			len(fmt.Sprint(got["object"])), got["object_truncated"], MaxObjectBytes)
	}
}

// decodeLine parses the single JSON line the recorder wrote.
func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got); err != nil {
		t.Fatalf("audit output is not JSON: %v\n%s", err, buf.String())
	}
	return got
}

// captureSink records every event it is handed.
type captureSink struct{ events []Event }

func (s *captureSink) Submit(ev Event) { s.events = append(s.events, ev) }

// lineFromEvent is what the JSON line must hold for ev: every field, under the
// line's key, in the type JSON decoding gives it.
func lineFromEvent(ev Event) map[string]any {
	return map[string]any{
		"msg":              "audit",
		"level":            "INFO",
		"request_id":       ev.RequestID,
		"authenticated":    ev.Authenticated,
		"auth_disabled":    ev.AuthDisabled,
		"subject":          ev.Subject,
		"source":           ev.Source,
		"method":           ev.Method,
		"route":            ev.Route,
		"object":           ev.Object,
		"object_truncated": ev.ObjectTruncated,
		"status":           float64(ev.Status),
		"outcome":          ev.Outcome,
		"latency_ms":       float64(ev.LatencyMS),
		"zone_scope":       ev.ZoneScope,
	}
}

// TestRecorderHandsTheSinkTheLoggedEvent pins the property persistence rests
// on: the sink gets exactly what the line says, field for field and to the
// nanosecond, for each way an entry is rendered.
func TestRecorderHandsTheSinkTheLoggedEvent(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
	}{
		{name: "authenticated", entry: Entry{RequestID: "R1", Authenticated: true, Subject: "operator",
			Source: "192.0.2.7", Method: "POST", Route: "/api/v1/qubes/:id/start", Object: "q-a",
			Status: 202, Outcome: OutcomeSuccess, LatencyMS: 3, ZoneScope: []string{"zone-a", "zone-b"}}},
		{name: "anonymous", entry: Entry{RequestID: "R2", Subject: "operator", ZoneScope: []string{"zone-a"},
			Method: "POST", Route: "/api/v1/zones", Status: 401, Outcome: OutcomeDenied}},
		{name: "auth disabled", entry: Entry{RequestID: "R3", AuthDisabled: true, Method: "DELETE",
			Route: "/api/v1/qubes/:id", Object: "q-b", Status: 204, Outcome: OutcomeSuccess}},
		{name: "truncated object", entry: Entry{RequestID: "R4", Method: "POST", Route: "/api/v1/qubes/:id/start",
			Object: strings.Repeat("é", MaxObjectBytes), Status: 401, Outcome: OutcomeDenied}},
		{name: "invalid utf-8", entry: Entry{RequestID: "R5", Authenticated: true, Subject: "op\xfferator",
			Method: "POST", Route: "/api/v1/qubes/:id/start", Object: "q\xff\xfe" + strings.Repeat("\xff", 2*MaxObjectBytes),
			Status: 401, Outcome: OutcomeDenied}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			sink := &captureSink{}
			at := time.Date(2026, 9, 26, 10, 11, 12, 123456789, time.UTC)
			rec := NewRecorder(&buf).WithSink(sink)
			rec.now = func() time.Time { return at }

			rec.Record(tc.entry)

			if len(sink.events) != 1 {
				t.Fatalf("sink got %d events, want 1", len(sink.events))
			}
			ev := sink.events[0]
			line := decodeLine(t, &buf)
			stamp, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["time"]))
			if err != nil || !stamp.Equal(ev.Time) || !ev.Time.Equal(at) {
				t.Errorf("line time %v (err %v), event time %v, want both %v", line["time"], err, ev.Time, at)
			}
			delete(line, "time")
			if want := lineFromEvent(ev); !reflect.DeepEqual(line, want) {
				t.Errorf("line and sink event disagree:\nline  %v\nevent %v", line, want)
			}
		})
	}
}

// TestValidUTF8ReplacesByteForByte pins the replacement to what the JSON
// encoder writes: one U+FFFD per invalid byte, valid text untouched.
func TestValidUTF8ReplacesByteForByte(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"q-a":              "q-a",
		"é€":               "é€",
		"a\xffb":           "a\uFFFDb",
		"\xff\xfe":         "\uFFFD\uFFFD",
		"\xe2\x82":         "\uFFFD\uFFFD", // a truncated three-byte sequence
		"ok\xe2\x82\xacok": "ok€ok",
	}
	for in, want := range cases {
		if got := validUTF8(in); got != want {
			t.Errorf("validUTF8(%q) = %q, want %q", in, got, want)
		}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded != validUTF8(in) {
			t.Errorf("JSON round trip of %q = %q, validUTF8 = %q", in, decoded, validUTF8(in))
		}
	}
}

// TestRecorderWritesAuthDisabled pins the new line field: the stored row keeps
// auth_disabled, so the line must carry it too.
func TestRecorderWritesAuthDisabled(t *testing.T) {
	var buf bytes.Buffer
	NewRecorder(&buf).Record(Entry{AuthDisabled: true})
	if got := decodeLine(t, &buf)["auth_disabled"]; got != true {
		t.Errorf("auth_disabled = %v, want true", got)
	}
}

// TestRecorderWithoutSinkOnlyLogs keeps the plain recorder a plain recorder,
// and WithSink a copy rather than a mutation of the original.
func TestRecorderWithoutSinkOnlyLogs(t *testing.T) {
	var buf bytes.Buffer
	plain := NewRecorder(&buf)
	sink := &captureSink{}
	_ = plain.WithSink(sink)

	plain.Record(Entry{RequestID: "R5"})

	if len(sink.events) != 0 {
		t.Errorf("WithSink changed the recorder it was called on")
	}
	if decodeLine(t, &buf)["request_id"] != "R5" {
		t.Errorf("the plain recorder must still write the line")
	}
}

// TestEventClassSamplesThrottlesAndUnprovenFailures pins which events the
// store samples (every 429, and unauthenticated failures) and which it always
// keeps.
func TestEventClassSamplesThrottlesAndUnprovenFailures(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		want Class
	}{
		{"authenticated success", Event{Authenticated: true, Outcome: OutcomeSuccess}, ClassFull},
		{"authenticated denial", Event{Authenticated: true, Outcome: OutcomeDenied}, ClassFull},
		{"authenticated client error", Event{Authenticated: true, Status: 400, Outcome: OutcomeClientError}, ClassFull},
		{"authenticated throttle", Event{Authenticated: true, Status: 429, Outcome: OutcomeClientError}, ClassSampled},
		{"session throttle", Event{Authenticated: true, Status: 429, Outcome: OutcomeClientError, ZoneScope: "zone-a"}, ClassSampled},
		{"login with a valid token", Event{Outcome: OutcomeSuccess}, ClassFull},
		{"auth disabled success", Event{AuthDisabled: true, Outcome: OutcomeSuccess}, ClassFull},
		{"anonymous denial", Event{Outcome: OutcomeDenied}, ClassSampled},
		{"anonymous throttle", Event{Status: 429, Outcome: OutcomeClientError}, ClassSampled},
		{"anonymous oversized body", Event{Status: 413, Outcome: OutcomeClientError}, ClassSampled},
		{"anonymous server error", Event{Outcome: OutcomeError}, ClassSampled},
		{"auth disabled failure", Event{AuthDisabled: true, Outcome: OutcomeClientError}, ClassSampled},
	}
	for _, tc := range cases {
		if got := tc.ev.Class(); got != tc.want {
			t.Errorf("%s: Class() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
