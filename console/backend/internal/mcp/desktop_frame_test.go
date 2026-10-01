package mcp

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/desktopaccess"
)

func encodeTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// frameServer answers the frame route with contentType and body, recording
// what it was sent.
func frameServer(rec *recorder, contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, req)
		rec.bodies = append(rec.bodies, string(b))
		rec.mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}

func contentBlock(t *testing.T, r Response) map[string]any {
	t.Helper()
	result, ok := r.Result.(map[string]any)
	if !ok {
		t.Fatalf("no result: %+v", r)
	}
	content := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", content)
	}
	return content[0].(map[string]any)
}

func TestToolsCall_DesktopFrameRequestsConsentThenReturnsTheImage(t *testing.T) {
	f := newFixture(t, ScopeControl, true)
	grants := &recorder{}
	frames := &recorder{}
	frame := encodeTestPNG(t, 2, 1)
	f.mux.HandleFunc("/api/v1/qubes/qube-1/desktop-access", grants.handle(http.StatusOK, `{"grant":"one-time-grant"}`))
	f.mux.HandleFunc("/api/v1/qubes/qube-1/desktop-frame", frameServer(frames, "image/png", frame))

	resp := f.call(t, "desktop_frame_get", `{"id":"qube-1"}`)
	if isErrorResult(resp) {
		t.Fatalf("desktop frame failed: %+v", resp)
	}
	block := contentBlock(t, resp)
	if block["type"] != "image" || block["mimeType"] != "image/png" {
		t.Fatalf("image block = %#v", block)
	}
	if _, hasText := block["text"]; hasText {
		t.Fatalf("an image block must not carry text: %#v", block)
	}
	if block["data"] != encodeBase64(frame) {
		t.Fatal("image data did not round-trip")
	}
	if grants.lastBody() != `{"operation":"frame"}` || frames.lastBody() != `{"grant":"one-time-grant"}` {
		t.Fatalf("request bodies: grant=%q frame=%q", grants.lastBody(), frames.lastBody())
	}
	if grants.last().Method != http.MethodPost || frames.last().Method != http.MethodPost {
		t.Fatal("both calls must be POST")
	}
	if got := frames.last().Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Fatalf("frame call Authorization = %q", got)
	}
}

// A refusal from the Console (the operator denied, the window closed, no
// transport) is passed through, and no frame is requested.
func TestToolsCall_DesktopFrameReportsARefusedRequest(t *testing.T) {
	f := newFixture(t, ScopeControl, true)
	frames := &recorder{}
	f.mux.HandleFunc("/api/v1/qubes/qube-1/desktop-access",
		(&recorder{}).handle(http.StatusForbidden, `{"error":"Forbidden","message":"the desktop access request was denied","code":403}`))
	f.mux.HandleFunc("/api/v1/qubes/qube-1/desktop-frame", frames.handle(http.StatusOK, ""))

	resp := f.call(t, "desktop_frame_get", `{"id":"qube-1"}`)
	text := resultText(t, resp)
	if !isErrorResult(resp) || !strings.Contains(text, "HTTP 403") || !strings.Contains(text, "denied") {
		t.Fatalf("text=%q isError=%v", text, isErrorResult(resp))
	}
	if frames.count() != 0 {
		t.Fatal("a refused request must not be followed by a frame call")
	}
}

func TestToolsCall_DesktopFrameRejectsBadUpstreamAnswers(t *testing.T) {
	valid := encodeTestPNG(t, 1, 1)
	for _, tc := range []struct {
		name, grantBody, frameType string
		frameStatus                int
		frameBody                  []byte
		want                       string
	}{
		{"no grant in the approval", `{"request_id":"r1"}`, "image/png", http.StatusOK, valid, "did not contain a grant"},
		{"approval not JSON", `grant`, "image/png", http.StatusOK, valid, "did not contain a grant"},
		{"frame not PNG", `{"grant":"g"}`, "text/html", http.StatusOK, valid, "was not a PNG"},
		{"frame bytes not a PNG", `{"grant":"g"}`, "image/png", http.StatusOK, []byte("<html>"), "was rejected"},
		{"frame refused", `{"grant":"g"}`, "application/json", http.StatusBadGateway, []byte(`{"message":"the qube's desktop could not be reached"}`), "HTTP 502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, ScopeControl, true)
			f.mux.HandleFunc("/api/v1/qubes/qube-1/desktop-access", (&recorder{}).handle(http.StatusOK, tc.grantBody))
			f.mux.HandleFunc("/api/v1/qubes/qube-1/desktop-frame", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.frameType)
				w.WriteHeader(tc.frameStatus)
				_, _ = w.Write(tc.frameBody)
			})
			resp := f.call(t, "desktop_frame_get", `{"id":"qube-1"}`)
			text := resultText(t, resp)
			if !isErrorResult(resp) || !strings.Contains(text, tc.want) {
				t.Fatalf("text=%q isError=%v, want %q", text, isErrorResult(resp), tc.want)
			}
		})
	}
}

func TestToolsCall_DesktopFrameAllowlistsTheQubeID(t *testing.T) {
	f := newFixture(t, ScopeControl, true)
	grants := &recorder{}
	f.mux.HandleFunc("/api/v1/", grants.handle(http.StatusOK, `{"grant":"g"}`))
	resp := f.call(t, "desktop_frame_get", `{"id":"../settings"}`)
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want invalid params, got %+v", resp)
	}
	if grants.count() != 0 {
		t.Fatal("an invalid id must not reach the Console")
	}
}

// The approval wait has its own bound: a client whose default timeout is far
// shorter than a person's decision still receives the grant, while ordinary
// tools keep the default.
func TestToolsCall_DesktopFrameOutlivesTheDefaultTimeout(t *testing.T) {
	f := newFixture(t, ScopeControl, true, WithTimeout(50*time.Millisecond))
	f.mux.HandleFunc("/api/v1/qubes/qube-1/desktop-access", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"grant":"g"}`)
	})
	f.mux.HandleFunc("/api/v1/qubes/qube-1/desktop-frame", frameServer(&recorder{}, "image/png", encodeTestPNG(t, 1, 1)))
	f.mux.HandleFunc("/api/v1/qubes/qube-1", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	})

	if resp := f.call(t, "desktop_frame_get", `{"id":"qube-1"}`); isErrorResult(resp) {
		t.Fatalf("desktop frame hit the default timeout: %+v", resp)
	}
	if resp := f.call(t, "qube_get", `{"id":"qube-1"}`); !isErrorResult(resp) {
		t.Fatal("an ordinary tool must still time out at the default")
	}
}

func TestDesktopFrameCallTimeoutCoversTheConsoleWindows(t *testing.T) {
	// The Console holds each call open for its window plus a 5 s write
	// margin; the tool must wait longer than that to see the answer.
	const consoleSlack = 5 * time.Second
	for _, window := range []time.Duration{desktopaccess.ApprovalTTL, desktopaccess.FrameGrantTTL} {
		if DesktopFrameCallTimeout <= window+consoleSlack {
			t.Fatalf("DesktopFrameCallTimeout %s does not cover a %s window plus %s", DesktopFrameCallTimeout, window, consoleSlack)
		}
	}
	if DefaultAPITimeout != 15*time.Second {
		t.Fatalf("DefaultAPITimeout = %s; the desktop wait must not raise the bound for every tool", DefaultAPITimeout)
	}
}

func TestContentItemJSONShapes(t *testing.T) {
	text, err := json.Marshal(ContentItem{Type: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if string(text) != `{"type":"text","text":""}` {
		t.Fatalf("empty text block = %s; text is required by the protocol", text)
	}
	img, err := json.Marshal(ContentItem{Type: "image", Data: "AAAA", MIMEType: "image/png", Text: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if string(img) != `{"type":"image","data":"AAAA","mimeType":"image/png"}` {
		t.Fatalf("image block = %s", img)
	}
	var back ContentItem
	if err := json.Unmarshal(img, &back); err != nil || back.Type != "image" || back.Data != "AAAA" || back.MIMEType != "image/png" {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	if err := json.Unmarshal([]byte(`{"type":1}`), &back); err == nil {
		t.Fatal("malformed block decoded")
	}
}
