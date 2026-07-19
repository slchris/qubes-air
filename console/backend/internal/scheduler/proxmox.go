package scheduler

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Credentials describe how to reach one Proxmox cluster.
//
// These are resolved from the console's encrypted credential store, not from
// the process environment: an operator manages clusters through the UI, and a
// credential that only exists as an env var cannot be rotated, audited, or
// scoped to a zone.
type Credentials struct {
	Endpoint string
	// APIToken is "user@realm!tokenid=secret". Preferred over username/password
	// because it can be scoped and revoked independently of a user account.
	APIToken string
	Username string
	Password string
	Insecure bool
}

// Valid reports whether the credentials are usable.
func (c Credentials) Valid() bool {
	return c.Endpoint != "" && (c.APIToken != "" || (c.Username != "" && c.Password != ""))
}

// CredentialResolver returns the credentials for a zone, decrypting them from
// the credential store. It is a function so the scheduler does not depend on
// the repository layer.
type CredentialResolver func(ctx context.Context, zoneID string) (Credentials, error)

// ProxmoxProvider reads live node capacity from the Proxmox cluster API.
type ProxmoxProvider struct {
	creds Credentials

	client *http.Client
	// ticket caches a password-auth session; tokens need no such dance.
	ticket   string
	ticketAt time.Time
}

// ticketTTL is how long a password-derived ticket is reused. Proxmox issues
// two-hour tickets; renewing well before that avoids racing the expiry.
const ticketTTL = 90 * time.Minute

// NewProxmoxProvider builds a capacity provider for one cluster.
func NewProxmoxProvider(creds Credentials) *ProxmoxProvider {
	creds.Endpoint = strings.TrimRight(creds.Endpoint, "/")
	return &ProxmoxProvider{
		creds: creds,
		// #nosec G402 -- InsecureSkipVerify is opt-in per credential and mirrors
		// the terraform provider's own switch for self-signed clusters.
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: creds.Insecure, MinVersion: tls.VersionTLS12},
			},
		},
	}
}

// clusterResource is the subset of /cluster/resources this needs.
type clusterResource struct {
	Node   string  `json:"node"`
	Type   string  `json:"type"`
	Status string  `json:"status"`
	MaxCPU int     `json:"maxcpu"`
	CPU    float64 `json:"cpu"`
	Mem    int64   `json:"mem"`
	MaxMem int64   `json:"maxmem"`
}

// Nodes returns live capacity for every node in the cluster.
func (p *ProxmoxProvider) Nodes(ctx context.Context) ([]NodeCapacity, error) {
	body, err := p.get(ctx, "/api2/json/cluster/resources?type=node")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []clusterResource `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode cluster resources: %w", err)
	}

	out := make([]NodeCapacity, 0, len(payload.Data))
	for _, r := range payload.Data {
		if r.Type != "node" {
			continue
		}
		out = append(out, NodeCapacity{
			Name:          r.Node,
			Online:        r.Status == "online",
			MaxCPU:        r.MaxCPU,
			CPUUsage:      r.CPU,
			MemUsedBytes:  r.Mem,
			MemTotalBytes: r.MaxMem,
		})
	}
	return out, nil
}

// get performs an authenticated GET against the cluster API.
func (p *ProxmoxProvider) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.creds.Endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	if err := p.authorize(ctx, req); err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxmox %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxmox %s: unexpected status %s", path, resp.Status)
	}
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
		if len(buf) > 8*1024*1024 {
			return nil, fmt.Errorf("proxmox %s: response too large", path)
		}
	}
	return buf, nil
}

// authorize attaches either the API token or a session ticket.
func (p *ProxmoxProvider) authorize(ctx context.Context, req *http.Request) error {
	if p.creds.APIToken != "" {
		// Proxmox uses the literal scheme word PVEAPIToken, not Bearer.
		req.Header.Set("Authorization", "PVEAPIToken="+p.creds.APIToken)
		return nil
	}
	if time.Since(p.ticketAt) > ticketTTL || p.ticket == "" {
		if err := p.login(ctx); err != nil {
			return err
		}
	}
	req.Header.Set("Cookie", "PVEAuthCookie="+p.ticket)
	return nil
}

// login exchanges username/password for a session ticket.
func (p *ProxmoxProvider) login(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", p.creds.Username)
	form.Set("password", p.creds.Password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.creds.Endpoint+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxmox login: unexpected status %s", resp.Status)
	}

	var payload struct {
		Data struct {
			Ticket string `json:"ticket"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("proxmox login: decode: %w", err)
	}
	if payload.Data.Ticket == "" {
		return fmt.Errorf("proxmox login: no ticket returned")
	}
	p.ticket = payload.Data.Ticket
	p.ticketAt = time.Now()
	return nil
}
