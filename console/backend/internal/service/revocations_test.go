package service

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestRevocationDocumentUsesExistingCAAndRegistry(t *testing.T) {
	issuer, certs, store := issuerFixture(t)
	_, err := issuer.RevocationDocument(context.Background())
	require.Error(t, err)
	require.Empty(t, store.creds, "public status reads must not mint a CA")
	ca, err := issuer.loadOrCreateCA(context.Background())
	require.NoError(t, err)
	bundle, err := ca.IssueAgentCert("console-test", time.Hour)
	require.NoError(t, err)
	require.NoError(t, certs.Register(context.Background(), &repository.AgentCert{
		Fingerprint: bundle.Fingerprint, QubeID: "relay", SubjectCN: "console-test", IssuedAt: time.Now(), ExpiresAt: &bundle.NotAfter,
	}))
	require.NoError(t, certs.Revoke(context.Background(), bundle.Fingerprint, "test reason must not be published"))
	doc, err := issuer.RevocationDocument(context.Background())
	require.NoError(t, err)
	state, err := pki.VerifyRevocations(doc, []*x509.Certificate{ca.Cert}, time.Now())
	require.NoError(t, err)
	require.Equal(t, []string{bundle.Fingerprint}, state.Revoked)
	require.NotContains(t, string(doc), "test reason")
}

func TestCloudInitCarriesRevocationURLWithoutInjection(t *testing.T) {
	_, id := renderFixture(t)
	pkg := testAgentPackage()
	pkg.RevocationURL = "https://console.example/pki/revocations"
	rendered, err := RenderAgentUserData("test", id, "", pkg, false)
	require.NoError(t, err)
	require.Contains(t, rendered, "QUBESAIR_REVOCATION_URL="+pkg.RevocationURL)
	pkg.RevocationURL = "http://console.example/\nQUBESAIR_ALLOW=everything"
	_, err = RenderAgentUserData("test", id, "", pkg, false)
	require.Error(t, err)
}
