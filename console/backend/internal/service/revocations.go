package service

import (
	"context"
	"time"

	"github.com/slchris/qubes-air/console/internal/repository"
)

// RevocationDocument reads existing CA material; a public read never creates
// or rotates a CA. Signed content contains only revoked certificate hashes.
func (c *CertIssuer) RevocationDocument(ctx context.Context) ([]byte, error) {
	ca, err := repository.LoadConsoleCA(ctx, c.creds)
	if err != nil {
		return nil, err
	}
	revoked, err := c.certs.RevokedFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	return ca.SignRevocations(revoked, time.Now())
}
