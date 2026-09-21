// Package proxmox implements the provider.Adapter contract against the Proxmox
// VE API. It replaces the bpg/proxmox terraform provider for the console's
// provisioning path.
//
// The API calls here are the same ones the terraform module described in
// terraform/modules/remote-qube-base/providers/proxmox/main.tf: a stopped
// storage-holder VM owns the persistent data disk, and an ephemeral compute VM
// clones the zone template, attaches that disk, delivers the agent identity as
// a cloud-init snippet, and starts.
package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/providerhttp"
)

// Config carries everything needed to talk to one Proxmox cluster.
//
// Credentials are resolved from the console's encrypted store by the caller and
// handed over as values: this package never reads the environment or a file,
// which keeps "where the secret came from" a single decision in the service
// layer.
type Config struct {
	// Endpoint is the HTTPS base URL; the hostname must match its certificate.
	Endpoint string
	// APIToken is "user@realm!tokenid=secret" when present; otherwise
	// Username/Password are used to obtain a session ticket.
	APIToken string
	Username string
	Password string
	// CAPEM optionally supplies the cluster CA instead of system roots.
	CAPEM string
	// Timeout bounds one HTTP request (not a whole task). Default 30s.
	Timeout time.Duration
}

// ticketTTL is how long a password-derived ticket is reused. Proxmox issues
// two-hour tickets; renewing well before that avoids racing the expiry.
const ticketTTL = 90 * time.Minute

// Client is a minimal, concurrency-safe Proxmox VE API client.
type Client struct {
	endpoint string
	token    string
	username string
	password string

	http *http.Client

	mu       sync.Mutex
	ticket   string
	csrf     string
	ticketAt time.Time
}

// NewClient builds a Client from cfg.
func NewClient(cfg Config) (*Client, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("proxmox: endpoint is required")
	}
	if cfg.APIToken == "" && (cfg.Username == "" || cfg.Password == "") {
		return nil, errors.New("proxmox: an API token or username/password is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport, err := providerhttp.NewClient(endpoint, cfg.CAPEM, timeout)
	if err != nil {
		return nil, err
	}
	return &Client{
		endpoint: endpoint,
		token:    cfg.APIToken,
		username: cfg.Username,
		password: cfg.Password,
		http:     transport,
	}, nil
}

// apiError is a non-2xx response. The request body is deliberately not echoed:
// it can carry credentials (a login form, an SSH key for cloud-init).
type apiError struct {
	Method string
	Path   string
	Status string
	Body   string
}

func (e *apiError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("proxmox %s %s: %s", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("proxmox %s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
}

// IsNotFound reports whether err means the resource is absent.
//
// A 404 is the obvious case. PVE also answers a status/config GET for a VM whose
// configuration file is gone with HTTP 500 and "Configuration file ... does not
// exist" — to PVE a missing config is a server-side inconsistency, not a routing
// miss. Treating only 404 as absent made destroy non-idempotent: tearing down a
// VM already removed (by an earlier run or by hand) failed, and the row stuck in
// "error" even though nothing was left to delete. Delete/purge must be
// idempotent, so this case has to read as absent.
func IsNotFound(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	if strings.HasPrefix(apiErr.Status, "404") {
		return true
	}
	return strings.HasPrefix(apiErr.Status, "500") && strings.Contains(apiErr.Body, "Configuration file ") &&
		strings.Contains(apiErr.Body, "/qemu-server/") && strings.Contains(apiErr.Body, "does not exist")
}

// get performs an authenticated GET and decodes the "data" envelope into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodGet, path, nil, out)
}

// post performs an authenticated POST with a form body.
func (c *Client) post(ctx context.Context, path string, form url.Values, out any) error {
	return c.request(ctx, http.MethodPost, path, form, out)
}

// put performs an authenticated PUT with a form body.
func (c *Client) put(ctx context.Context, path string, form url.Values, out any) error {
	return c.request(ctx, http.MethodPut, path, form, out)
}

// delete performs an authenticated DELETE.
func (c *Client) delete(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodDelete, path, nil, out)
}

// request is the single authenticated call path, so authentication and envelope
// decoding cannot be bypassed.
func (c *Client) request(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return fmt.Errorf("proxmox: build %s %s: %w", method, path, err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if err := c.authorize(ctx, req, method); err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap the body: an unexpected error page should not be able to exhaust
	// memory. 8 MiB is far above any API response this client expects.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("proxmox %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{Method: method, Path: path, Status: resp.Status, Body: truncate(raw, 512)}
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("proxmox %s %s: decode envelope: %w", method, path, err)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("proxmox %s %s: decode data: %w", method, path, err)
	}
	return nil
}

// authorize attaches either the API token or a session ticket. The ticket path
// needs a CSRF header on writes; the token path does not.
func (c *Client) authorize(ctx context.Context, req *http.Request, method string) error {
	if c.token != "" {
		req.Header.Set("Authorization", "PVEAPIToken="+c.token)
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ticket == "" || time.Since(c.ticketAt) > ticketTTL {
		if err := c.loginLocked(ctx); err != nil {
			return err
		}
	}
	req.Header.Set("Cookie", "PVEAuthCookie="+c.ticket)
	if method != http.MethodGet {
		req.Header.Set("CSRFPreventionToken", c.csrf)
	}
	return nil
}

// loginLocked exchanges username/password for a session ticket. The caller must
// hold c.mu.
func (c *Client) loginLocked(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("proxmox login: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &apiError{Method: http.MethodPost, Path: "/api2/json/access/ticket", Status: resp.Status}
	}
	var payload struct {
		Data struct {
			Ticket string `json:"ticket"`
			CSRF   string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("proxmox login: decode: %w", err)
	}
	if payload.Data.Ticket == "" {
		return errors.New("proxmox login: no ticket returned")
	}
	c.ticket = payload.Data.Ticket
	c.csrf = payload.Data.CSRF
	c.ticketAt = time.Now()
	return nil
}

// taskStatus is the subset of /tasks/<upid>/status this client reads.
type taskStatus struct {
	Status     string `json:"status"`
	ExitStatus string `json:"exitstatus"`
}

// WaitTask polls an asynchronous task until it finishes or ctx expires.
//
// Proxmox mutate endpoints return a UPID and do the work in the background;
// treating the HTTP 200 as "done" is how a clone is reported successful before
// the VM exists. exitstatus "OK" is the only success value.
func (c *Client) WaitTask(ctx context.Context, node, upid string, poll time.Duration) error {
	if node == "" || upid == "" {
		return errors.New("proxmox: WaitTask needs node and upid")
	}
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		var st taskStatus
		err := c.get(ctx, "/api2/json/nodes/"+url.PathEscape(node)+"/tasks/"+url.PathEscape(upid)+"/status", &st)
		if err != nil {
			return err
		}
		if st.Status == vmStatusStopped {
			if st.ExitStatus == "OK" {
				return nil
			}
			return fmt.Errorf("proxmox task %s failed: %s", upid, st.ExitStatus)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("proxmox task %s did not finish: %w", upid, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// truncate bounds a message to n bytes without splitting a rune.
func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
