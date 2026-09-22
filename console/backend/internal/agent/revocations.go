package agent

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// RevocationRegistry retrieves CA-signed state without bearer credentials.
// Fetching fails closed. Cached successful reads last at most 15 seconds;
// signed snapshots are also checked against their absolute expiry each time.
type RevocationRegistry struct {
	gate     chan struct{}
	endpoint string
	roots    []*x509.Certificate
	client   *http.Client
	state    *pki.RevocationState
	fetched  time.Time
}

// NewRevocationRegistry requires an explicit HTTP(S) status endpoint. HTTP is
// permitted because the pinned CA signature authenticates the payload and no
// credentials are sent; redirects and non-HTTP schemes are rejected.
func NewRevocationRegistry(endpoint string, roots []*x509.Certificate) (*RevocationRegistry, error) {
	if err := pki.ValidateRevocationURL(endpoint); err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, errors.New("agent requires a trusted CA")
	}
	return &RevocationRegistry{gate: make(chan struct{}, 1), endpoint: endpoint, roots: roots, client: &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("revocation redirects are forbidden")
		},
	}}, nil
}

// Authorize implements the transport's registry contract. Certificate role,
// chain, purpose and validity are checked independently by the TLS server.
func (r *RevocationRegistry) Authorize(ctx context.Context, fingerprint string) (*repository.AgentCert, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	now := time.Now()
	if r.state == nil || now.Sub(r.fetched) >= 15*time.Second || !now.Before(r.state.ExpiresAt) {
		if err := r.refresh(ctx, now); err != nil {
			return nil, fmt.Errorf("revocation status unavailable: %w", err)
		}
	}
	for _, revoked := range r.state.Revoked {
		if revoked == fingerprint {
			return nil, repository.ErrCertRevoked
		}
	}
	return &repository.AgentCert{Fingerprint: fingerprint}, nil
}

// TouchLastSeen is local-only: agents do not send usage data to a public feed.
func (*RevocationRegistry) TouchLastSeen(context.Context, string) error { return nil }

func (r *RevocationRegistry) refresh(ctx context.Context, now time.Time) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint, nil)
	if err != nil {
		return err
	}
	response, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("revocation source returned an error")
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil {
		return err
	}
	state, err := pki.VerifyRevocations(document, r.roots, time.Now())
	if err != nil {
		return err
	}
	if r.state != nil && state.IssuedAt.Before(r.state.IssuedAt) {
		return errors.New("revocation rollback refused")
	}
	r.state, r.fetched = state, now
	return nil
}
