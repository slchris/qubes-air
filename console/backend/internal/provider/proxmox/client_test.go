package proxmox

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCalls records every request the adapter makes, for assertions.
type fakeCalls struct {
	mu     sync.Mutex
	calls  []string
	bodies map[string]string
}

func (c *fakeCalls) add(r *http.Request) {
	body := ""
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, r.Method+" "+r.URL.Path)
	if c.bodies == nil {
		c.bodies = make(map[string]string)
	}
	c.bodies[r.Method+" "+r.URL.Path] = body
}

func (c *fakeCalls) has(method, path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		if call == method+" "+path {
			return true
		}
	}
	return false
}

// body returns the last request body recorded for method+path.
func (c *fakeCalls) body(method, path string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[method+" "+path]
}

func writeData(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": v})
}

const testUPID = "UPID:infra-node1:00000000:00000000:00000000:qmcreate:100:root@pam:"

// taskStatusHandler answers any task-status poll with OK.
func taskStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && len(r.URL.Path) > len("/status") &&
		r.URL.Path[len(r.URL.Path)-len("/status"):] == "/status" {
		writeData(w, map[string]string{"status": vmStatusStopped, "exitstatus": "OK"})
		return
	}
	http.Error(w, "unexpected", http.StatusNotFound)
}

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *fakeCalls) {
	t.Helper()
	rec := &fakeCalls{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(Config{Endpoint: srv.URL, CAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})), APIToken: "root@pam!tok=secret", Timeout: 5 * time.Second})
	require.NoError(t, err)
	return client, rec
}

func TestClient_PasswordLoginThenGet(t *testing.T) {
	client, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/access/ticket":
			writeData(w, map[string]string{"ticket": "PVE:root@pam:ticket", "CSRFPreventionToken": "csrf"})
		case "/api2/json/version":
			writeData(w, map[string]any{"version": "9.2.10", "release": "9.2"})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	client.token = ""
	client.username = "user@pve"
	client.password = "pw"

	var out map[string]any
	require.NoError(t, client.get(context.Background(), "/api2/json/version", &out))
	assert.Equal(t, "9.2.10", out["version"])
	assert.True(t, rec.has("POST", "/api2/json/access/ticket"))
	assert.True(t, rec.has("GET", "/api2/json/version"))
}

func TestClient_ErrorOmitsRequestBody(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":{"password":"bad"}}`, http.StatusUnauthorized)
	})
	client.token = ""
	client.username = "user@pve"
	client.password = "topsecret"

	err := client.get(context.Background(), "/api2/json/version", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "topsecret", "credentials must never appear in an error")
}

func TestWaitTask_Success(t *testing.T) {
	client, _ := newTestClient(t, taskStatusHandler)
	require.NoError(t, client.WaitTask(context.Background(), "infra-node1", testUPID, time.Millisecond))
}

func TestWaitTask_Failure(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(w, map[string]string{"status": vmStatusStopped, "exitstatus": "clone failed: no space"})
	})
	err := client.WaitTask(context.Background(), "infra-node1", testUPID, time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clone failed")
}

// TestIsNotFound treats a missing VM config as absent.
//
// PVE answers a status GET for a VM whose config file is gone with HTTP 500 and
// "Configuration file ... does not exist", not 404. If that case is not
// recognized, destroy/purge stops being idempotent and a row whose resources are
// already gone sticks in "error".
func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"404", &apiError{Status: "404 Not Found", Body: "no such vm"}, true},
		{"500 missing config",
			&apiError{Status: "500 Internal Server Error",
				Body: "Configuration file 'nodes/infra-node4/qemu-server/105.conf' does not exist"}, true},
		{"500 other", &apiError{Status: "500 Internal Server Error", Body: "clone failed"}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := IsNotFound(tc.err); got != tc.want {
			t.Errorf("%s: IsNotFound = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestClientRejectsPlaintextEndpoint(t *testing.T) {
	_, err := NewClient(Config{Endpoint: "http://127.0.0.1:12345", APIToken: "test!id=value"})
	require.Error(t, err)
}
