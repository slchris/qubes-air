package service

import (
	"context"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// RevocationDocument reads existing CA material; a public read never creates
// or rotates a CA. Signed content contains only revoked certificate hashes.
func (c *CertIssuer) RevocationDocument(ctx context.Context) ([]byte, error) {
	cert, err := lookupCredential(ctx, c.creds, caCertCredentialName)
	if err != nil {
		return nil, err
	}
	key, err := lookupCredential(ctx, c.creds, caKeyCredentialName)
	if err != nil {
		return nil, err
	}
	ca, err := pki.ParseCA(cert, key)
	if err != nil {
		return nil, err
	}
	revoked, err := c.certs.RevokedFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	return ca.SignRevocations(revoked, time.Now())
}
