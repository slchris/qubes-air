package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAPITimeout bounds every Console API request. Qubes Air server
	// writes cap at 15s; anything slower is a broken upstream, not a long op.
	// Long operations are queued as jobs and polled, never awaited inline.
	DefaultAPITimeout = 15 * time.Second
	// DefaultMaxResponseBody caps how much of an upstream response the MCP
	// process will hold in memory and forward to the client.
	DefaultMaxResponseBody = 8 << 20 // 8 MiB
)

// APIError reports a non-2xx response from the Console API. It never contains
// the bearer token or connection details — only what a caller may learn from
// the response itself.
type APIError struct {
	StatusCode int
	Body       []byte
}

// Error renders the status and (truncated) upstream message.
func (e *APIError) Error() string {
	body := strings.TrimSpace(string(e.Body))
	if len(body) > 512 {
		body = body[:512] + "..."
	}
	if body == "" {
		return fmt.Sprintf("upstream API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("upstream API returned HTTP %d: %s", e.StatusCode, body)
}

// APIResponse is an HTTP response from the Console API that completed at the
// transport level. Non-2xx status codes are not errors here: the caller reads
// StatusCode and passes the outcome through to the MCP client.
type APIResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Client is a loopback HTTP client for the Console API.
//
// The bearer token is sent in the Authorization header only. It is deliberately
// absent from every error message this package constructs, and the Client never
// logs, so a misbehaving tool call cannot spill a credential into the MCP
// client or the process logs.
type Client struct {
	baseURL string
	token   string
	client  *http.Client
	maxBody int64
}

// ClientOption configures a Client. Options exist so tests can inject a
// RoundTripper and so the operator can tune timeouts and body caps.
type ClientOption func(*Client)

// WithTimeout overrides the default per-request timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) { c.client.Timeout = d }
}

// WithTransport replaces the underlying HTTP transport (used by tests).
func WithTransport(rt http.RoundTripper) ClientOption {
	return func(c *Client) { c.client.Transport = rt }
}

// WithMaxResponseBody overrides the default response body cap.
func WithMaxResponseBody(n int64) ClientOption {
	return func(c *Client) { c.maxBody = n }
}

// NewClient creates a Console API client for baseURL. token may be empty when
// the Console API runs with authentication disabled.
func NewClient(baseURL, token string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client: &http.Client{
			Timeout: DefaultAPITimeout,
			// The Console API is reached directly. A redirect would aim this
			// process — and the Authorization header it carries — at a host the
			// operator never named, so follow none of them.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return fmt.Errorf("refusing redirect to %s: the Console API is reached directly", req.URL.Host)
			},
		},
		maxBody: DefaultMaxResponseBody,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Do performs one Console API request.
//
// path is already URL-encoded (callers build it from allowlisted inputs);
// query is encoded here. body, when non-nil, is JSON-marshaled. Transport
// failures (dial, timeout, body over the cap) return an error that carries no
// credentials. A non-2xx status arrives as an *APIResponse with StatusCode set.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (*APIResponse, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var rdr io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		rdr = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := c.readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	return &APIResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       payload,
	}, nil
}

// readBody reads the response up to the cap, failing rather than buffering an
// unbounded payload from a compromised or buggy upstream.
func (c *Client) readBody(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, c.maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(data)) > c.maxBody {
		return nil, fmt.Errorf("response body exceeds %d bytes", c.maxBody)
	}
	return data, nil
}
