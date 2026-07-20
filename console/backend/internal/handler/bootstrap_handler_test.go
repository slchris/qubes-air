package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
)

type fakeRedeemer struct {
	tok      *repository.BootstrapToken
	err      error
	redeemed []string
}

func (f *fakeRedeemer) Redeem(_ context.Context, secret string, _ time.Time) (*repository.BootstrapToken, error) {
	f.redeemed = append(f.redeemed, secret)
	if f.err != nil {
		return nil, f.err
	}
	return f.tok, nil
}

type fakeSigner struct {
	gotCN  string
	gotCSR string
	err    error
}

func (f *fakeSigner) SignAgentCSR(
	_ context.Context, csrPEM, wantCN string, _ time.Duration,
) (*service.SignedAgentCert, error) {
	f.gotCN, f.gotCSR = wantCN, csrPEM
	if f.err != nil {
		return nil, f.err
	}
	return &service.SignedAgentCert{
		CertPEM: "-----BEGIN CERTIFICATE-----\nsigned\n-----END CERTIFICATE-----",
		CAPEM:   "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
		// 16+ chars: the handler logs Fingerprint[:16].
		Fingerprint: "abcdef0123456789abcdef",
		NotAfter:    time.Now().Add(90 * 24 * time.Hour).UTC(),
		SubjectCN:   wantCN,
	}, nil
}

type fakeRegistrar struct {
	registered []*repository.AgentCert
	err        error
}

func (f *fakeRegistrar) Register(_ context.Context, c *repository.AgentCert) error {
	if f.err != nil {
		return f.err
	}
	f.registered = append(f.registered, c)
	return nil
}

func bootstrapRig(t *testing.T, red *fakeRedeemer, sig *fakeSigner, reg *fakeRegistrar) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewBootstrapHandler(red, sig, reg).RegisterRoutes(r.Group("/bootstrap"))
	return r
}

func post(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/bootstrap/certificate", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func liveToken() *repository.BootstrapToken {
	return &repository.BootstrapToken{QubeID: "qube-1", QubeName: "remote-dev"}
}

func TestBootstrapIssuesAgainstAValidToken(t *testing.T) {
	red, sig, reg := &fakeRedeemer{tok: liveToken()}, &fakeSigner{}, &fakeRegistrar{}
	w := post(t, bootstrapRig(t, red, sig, reg), `{"token":"t","csr_pem":"CSR"}`)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got BootstrapCertResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Contains(t, got.CertPEM, "signed")
	assert.Contains(t, got.CAPEM, "ca")
	assert.Equal(t, "agent-remote-dev", got.SubjectCN)
}

// The response must never carry a private key — the point of the whole endpoint
// is that the key stayed on the agent.
func TestBootstrapResponseCarriesNoPrivateKey(t *testing.T) {
	red, sig, reg := &fakeRedeemer{tok: liveToken()}, &fakeSigner{}, &fakeRegistrar{}
	w := post(t, bootstrapRig(t, red, sig, reg), `{"token":"t","csr_pem":"CSR"}`)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	for _, marker := range []string{"PRIVATE KEY", "key_pem", "keyPEM"} {
		assert.NotContains(t, body, marker,
			"the issued identity must contain no private key material")
	}
}

// The common name must come from the redeemed record. If the request body could
// name the identity, any valid token would mint a certificate for the most
// valuable qube in the fleet.
func TestCommonNameComesFromTheTokenNotTheRequest(t *testing.T) {
	red, sig, reg := &fakeRedeemer{tok: liveToken()}, &fakeSigner{}, &fakeRegistrar{}
	w := post(t, bootstrapRig(t, red, sig, reg),
		`{"token":"t","csr_pem":"CSR","qube_name":"remote-prod","subject_cn":"agent-remote-prod"}`)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "agent-remote-dev", sig.gotCN,
		"the signer was asked for a name the caller supplied")
}

// Unknown, expired and spent must be indistinguishable to the caller. Saying
// which tells an attacker what to try next.
func TestRejectedTokenLeaksNoDetail(t *testing.T) {
	detailed := errors.New(
		"bootstrap token not accepted: the token for \"remote-dev\" expired at 2026-01-01T00:00:00Z")
	red := &fakeRedeemer{err: errors.Join(repository.ErrBootstrapTokenRejected, detailed)}
	sig, reg := &fakeSigner{}, &fakeRegistrar{}

	w := post(t, bootstrapRig(t, red, sig, reg), `{"token":"nope","csr_pem":"CSR"}`)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "bootstrap token not accepted")
	for _, leak := range []string{"expired", "remote-dev", "redeemed", "2026-01-01"} {
		assert.NotContains(t, body, leak,
			"the response told the caller why the token failed")
	}
	assert.Empty(t, sig.gotCSR, "a refused token must not reach the signer")
}

// Fail closed: the certificate must be registered before it is handed over, or
// the agent receives a credential the server will refuse.
func TestCertificateIsRegisteredBeforeItIsReturned(t *testing.T) {
	red, sig := &fakeRedeemer{tok: liveToken()}, &fakeSigner{}
	reg := &fakeRegistrar{err: errors.New("registry is down")}

	w := post(t, bootstrapRig(t, red, sig, reg), `{"token":"t","csr_pem":"CSR"}`)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "BEGIN CERTIFICATE",
		"a certificate that could not be registered was handed out anyway")
}

func TestRegisteredCertificateIsBoundToTheTokensQube(t *testing.T) {
	red, sig, reg := &fakeRedeemer{tok: liveToken()}, &fakeSigner{}, &fakeRegistrar{}
	post(t, bootstrapRig(t, red, sig, reg), `{"token":"t","csr_pem":"CSR"}`)

	require.Len(t, reg.registered, 1)
	assert.Equal(t, "qube-1", reg.registered[0].QubeID)
	assert.Equal(t, "agent-remote-dev", reg.registered[0].SubjectCN)
	require.NotNil(t, reg.registered[0].ExpiresAt)
}

// A signing failure has already spent the token. The handler must not pretend
// otherwise by succeeding, and must not retry.
func TestSigningFailureDoesNotIssueAnything(t *testing.T) {
	red, reg := &fakeRedeemer{tok: liveToken()}, &fakeRegistrar{}
	sig := &fakeSigner{err: errors.New("CSR common name does not match")}

	w := post(t, bootstrapRig(t, red, sig, reg), `{"token":"t","csr_pem":"CSR"}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, reg.registered)
	assert.Len(t, red.redeemed, 1, "the token must be redeemed exactly once, not retried")
}

func TestMissingCSRIsRefusedBeforeTheTokenIsSpent(t *testing.T) {
	red, sig, reg := &fakeRedeemer{tok: liveToken()}, &fakeSigner{}, &fakeRegistrar{}
	w := post(t, bootstrapRig(t, red, sig, reg), `{"token":"t"}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, red.redeemed,
		"a malformed request must not burn a token the caller may still need")
}

func TestMalformedBodyIsRefused(t *testing.T) {
	red, sig, reg := &fakeRedeemer{tok: liveToken()}, &fakeSigner{}, &fakeRegistrar{}
	w := post(t, bootstrapRig(t, red, sig, reg), `not json`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, red.redeemed)
}

// The endpoint is unauthenticated by design, so it must not let an anonymous
// caller hand over an unbounded body.
func TestOversizedBodyIsRefused(t *testing.T) {
	red, sig, reg := &fakeRedeemer{tok: liveToken()}, &fakeSigner{}, &fakeRegistrar{}
	huge := `{"token":"t","csr_pem":"` + strings.Repeat("A", maxBootstrapBody+1) + `"}`

	w := post(t, bootstrapRig(t, red, sig, reg), huge)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, red.redeemed)
}
