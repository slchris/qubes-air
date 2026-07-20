// bootstrap_test.go — the ordering guarantees of first-certificate issuance.
//
// Ported from the HTTP handler's tests when the transport was removed
// (docs/bootstrap-design.md §9.3); the properties under test never belonged to
// HTTP. The counterfactuals that matter: the CN comes from the token, the
// registry is written before the certificate is released, and a request that
// could never be signed does not burn the token.
package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/repository"
)

type fakeTokenRedeemer struct {
	tok      *repository.BootstrapToken
	err      error
	redeemed []string
}

func (f *fakeTokenRedeemer) Redeem(_ context.Context, secret string, _ time.Time) (*repository.BootstrapToken, error) {
	f.redeemed = append(f.redeemed, secret)
	if f.err != nil {
		return nil, f.err
	}
	return f.tok, nil
}

type fakeCSRSigner struct {
	gotCN  string
	gotCSR string
	err    error
}

func (f *fakeCSRSigner) SignAgentCSR(
	_ context.Context, csrPEM, wantCN string, _ time.Duration,
) (*SignedAgentCert, error) {
	f.gotCN, f.gotCSR = wantCN, csrPEM
	if f.err != nil {
		return nil, f.err
	}
	return &SignedAgentCert{
		CertPEM:     "-----BEGIN CERTIFICATE-----\nsigned\n-----END CERTIFICATE-----",
		CAPEM:       "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
		Fingerprint: "abcdef0123456789abcdef",
		NotAfter:    time.Now().Add(90 * 24 * time.Hour).UTC(),
		SubjectCN:   wantCN,
	}, nil
}

type fakeCertRegistrar struct {
	registered []*repository.AgentCert
	err        error
}

func (f *fakeCertRegistrar) Register(_ context.Context, c *repository.AgentCert) error {
	if f.err != nil {
		return f.err
	}
	f.registered = append(f.registered, c)
	return nil
}

func liveBootstrapToken() *repository.BootstrapToken {
	return &repository.BootstrapToken{QubeID: "qube-1", QubeName: "remote-dev"}
}

func TestBootstrapIssuesAgainstAValidToken(t *testing.T) {
	red, sig, reg := &fakeTokenRedeemer{tok: liveBootstrapToken()}, &fakeCSRSigner{}, &fakeCertRegistrar{}
	issuer := NewBootstrapIssuer(red, sig, reg)

	got, err := issuer.IssueFirstCertificate(context.Background(), "t", makeCSR(t, "agent-remote-dev"))
	require.NoError(t, err)
	assert.Contains(t, got.CertPEM, "signed")
	assert.Contains(t, got.CAPEM, "ca")
	assert.Equal(t, "agent-remote-dev", got.SubjectCN)
	assert.Equal(t, "qube-1", got.QubeID)
}

// The common name must come from the redeemed record, not from anything the
// request carried. The CSR here names another qube; the signer must still be
// asked for the token's name — the signer's own CN check is what then refuses
// the mismatch, AFTER the token is spent, because a valid token attached to a
// CSR for someone else's name is an escalation attempt, not a typo.
func TestCommonNameComesFromTheTokenNotTheCSR(t *testing.T) {
	red, sig, reg := &fakeTokenRedeemer{tok: liveBootstrapToken()}, &fakeCSRSigner{}, &fakeCertRegistrar{}
	issuer := NewBootstrapIssuer(red, sig, reg)

	_, err := issuer.IssueFirstCertificate(context.Background(), "t", makeCSR(t, "agent-remote-prod"))
	require.NoError(t, err, "the fake signer does not enforce CN; the real one does")
	assert.Equal(t, "agent-remote-dev", sig.gotCN,
		"the signer was asked for a name derived from something other than the token")
}

// A rejected token must not reach the signer.
func TestRejectedTokenNeverReachesTheSigner(t *testing.T) {
	red := &fakeTokenRedeemer{err: repository.ErrBootstrapTokenRejected}
	sig, reg := &fakeCSRSigner{}, &fakeCertRegistrar{}
	issuer := NewBootstrapIssuer(red, sig, reg)

	_, err := issuer.IssueFirstCertificate(context.Background(), "nope", makeCSR(t, "agent-remote-dev"))
	require.ErrorIs(t, err, repository.ErrBootstrapTokenRejected)
	assert.Empty(t, sig.gotCSR, "a refused token must not reach the signer")
	assert.Empty(t, reg.registered)
}

// Fail closed: the certificate must be registered before it is handed over, or
// the agent receives a credential the server will refuse.
func TestCertificateIsRegisteredBeforeItIsReturned(t *testing.T) {
	red, sig := &fakeTokenRedeemer{tok: liveBootstrapToken()}, &fakeCSRSigner{}
	reg := &fakeCertRegistrar{err: errors.New("registry is down")}
	issuer := NewBootstrapIssuer(red, sig, reg)

	got, err := issuer.IssueFirstCertificate(context.Background(), "t", makeCSR(t, "agent-remote-dev"))
	require.Error(t, err)
	assert.Nil(t, got, "a certificate that could not be registered was handed out anyway")
}

func TestRegisteredCertificateIsBoundToTheTokensQube(t *testing.T) {
	red, sig, reg := &fakeTokenRedeemer{tok: liveBootstrapToken()}, &fakeCSRSigner{}, &fakeCertRegistrar{}
	issuer := NewBootstrapIssuer(red, sig, reg)

	_, err := issuer.IssueFirstCertificate(context.Background(), "t", makeCSR(t, "agent-remote-dev"))
	require.NoError(t, err)
	require.Len(t, reg.registered, 1)
	assert.Equal(t, "qube-1", reg.registered[0].QubeID)
	assert.Equal(t, "agent-remote-dev", reg.registered[0].SubjectCN)
	require.NotNil(t, reg.registered[0].ExpiresAt)
}

// A signing failure has already spent the token. The issuer must not pretend
// otherwise by succeeding, and must not retry.
func TestSigningFailureDoesNotIssueAnything(t *testing.T) {
	red, reg := &fakeTokenRedeemer{tok: liveBootstrapToken()}, &fakeCertRegistrar{}
	sig := &fakeCSRSigner{err: errors.New("CSR common name does not match")}
	issuer := NewBootstrapIssuer(red, sig, reg)

	_, err := issuer.IssueFirstCertificate(context.Background(), "t", makeCSR(t, "agent-remote-prod"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrBootstrapCSRInvalid,
		"a post-redemption failure must not claim the token is still live")
	assert.Empty(t, reg.registered)
	assert.Len(t, red.redeemed, 1, "the token must be redeemed exactly once, not retried")
}

// The counterfactual this file exists for: every shape of unusable CSR is
// refused BEFORE the token is spent. The old HTTP handler only checked that
// the field was non-empty, so a garbage csr_pem burned the token and left the
// qube stuck until an operator minted another.
func TestUnusableCSRIsRefusedBeforeTheTokenIsSpent(t *testing.T) {
	tampered := makeCSR(t, "agent-remote-dev")
	// Corrupt base64 payload inside the PEM body so the DER no longer parses.
	tampered = strings.Replace(tampered, "\n", "\nAAAA", 1)

	cases := map[string]string{
		"empty":          "",
		"not PEM at all": "definitely not a certificate request",
		"wrong PEM type": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----",
		"corrupted DER":  tampered,
		"carries SANs":   makeCSR(t, "agent-remote-dev", "sneaky.example.com"),
	}
	for name, csr := range cases {
		t.Run(name, func(t *testing.T) {
			red, sig, reg := &fakeTokenRedeemer{tok: liveBootstrapToken()}, &fakeCSRSigner{}, &fakeCertRegistrar{}
			issuer := NewBootstrapIssuer(red, sig, reg)

			_, err := issuer.IssueFirstCertificate(context.Background(), "t", csr)
			require.ErrorIs(t, err, ErrBootstrapCSRInvalid)
			assert.Empty(t, red.redeemed,
				"an unusable CSR must not burn a token the agent may still need")
			assert.Empty(t, sig.gotCSR)
			assert.Empty(t, reg.registered)
		})
	}
}

// The issued identity carries no private key, structurally: the type has no
// field for one. This test pins the property against the type growing one.
func TestIssuedBootstrapCertHasNoKeyMaterial(t *testing.T) {
	red, sig, reg := &fakeTokenRedeemer{tok: liveBootstrapToken()}, &fakeCSRSigner{}, &fakeCertRegistrar{}
	issuer := NewBootstrapIssuer(red, sig, reg)

	got, err := issuer.IssueFirstCertificate(context.Background(), "t", makeCSR(t, "agent-remote-dev"))
	require.NoError(t, err)
	for _, s := range []string{got.CertPEM, got.CAPEM, got.Fingerprint, got.SubjectCN} {
		assert.NotContains(t, s, "PRIVATE KEY",
			"the issued identity must contain no private key material")
	}
}
