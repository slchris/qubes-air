package mcp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testToken = "s3cr3t-test-token-do-not-leak"

// closedLoopbackAddr returns a loopback address with no listener behind it.
func closedLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestClient_SendsBearerTokenOnEveryRequest(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"status":"ok"}`)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, testToken)
	resp, err := c.Do(context.Background(), http.MethodGet, "/api/v1/status", nil, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if gotAuth != "Bearer "+testToken {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestClient_OmitsAuthHeaderWithoutToken(t *testing.T) {
	var gotAuth = "unset"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "")
	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuth)
	}
}

func TestClient_EncodesQueryParameters(t *testing.T) {
	var gotRaw string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "{}")
	}))
	defer ts.Close()

	c := NewClient(ts.URL, testToken)
	query := url.Values{"status": {"running"}, "type": {"app work"}}
	if _, err := c.Do(context.Background(), http.MethodGet, "/api/v1/qubes", query, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !strings.Contains(gotRaw, "status=running") || !strings.Contains(gotRaw, "type=app+work") {
		t.Fatalf("RawQuery = %q", gotRaw)
	}
}

func TestClient_MarshalsJSONBody(t *testing.T) {
	var gotBody string
	var gotCT string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "{}")
	}))
	defer ts.Close()

	c := NewClient(ts.URL, testToken)
	if _, err := c.Do(context.Background(), http.MethodPost, "/api/v1/qubes", nil, map[string]any{"name": "n"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !strings.Contains(gotBody, `"name":"n"`) {
		t.Fatalf("body = %q", gotBody)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
}

func TestClient_Non2xxStatusIsNotATransportError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"bad params"}`)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, testToken)
	resp, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err != nil {
		t.Fatalf("Do must not error on non-2xx: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "bad params") {
		t.Fatalf("body = %q", resp.Body)
	}
}

func TestClient_ErrorsNeverContainTheToken(t *testing.T) {
	// Transport error through a refused connection.
	c := NewClient("http://"+closedLoopbackAddr(t), testToken)
	_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("transport error leaked the token: %v", err)
	}

	// HTTP error body built from upstream, not from the client.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"forbidden"}`)
	}))
	defer ts.Close()
	c = NewClient(ts.URL, testToken)
	resp, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestClient_ResponseBodyCapIsEnforced(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", 200))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, testToken, WithMaxResponseBody(100))
	_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("expected a body-cap error")
	}
	if !strings.Contains(err.Error(), "100") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("body-cap error leaked the token: %v", err)
	}
}

func TestClient_RequestTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, testToken, WithTimeout(60*time.Millisecond))
	_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("timeout error leaked the token: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a deadline-exceeded error, got %v", err)
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := NewClient(ts.URL, testToken, WithTimeout(10*time.Second))
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := c.Do(ctx, http.MethodGet, "/x", nil, nil); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
