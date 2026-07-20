package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/agent"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// fakeBootstrapAgent answers the two bootstrap calls the way a real agent would, and
// records what it was told.
type fakeBootstrapAgent struct {
	beginReply  any
	beginErr    error
	installedFP string
	completeErr error

	gotComplete completeBootstrapRequest
	calls       []string
}

func (f *fakeBootstrapAgent) address() string { return "10.0.0.9:8443" }

func (f *fakeBootstrapAgent) call(_ context.Context, _, service string, in []byte) ([]byte, error) {
	f.calls = append(f.calls, service)
	switch service {
	case beginBootstrapService:
		if f.beginErr != nil {
			return nil, f.beginErr
		}
		return json.Marshal(f.beginReply)
	case completeBootstrapService:
		if err := json.Unmarshal(in, &f.gotComplete); err != nil {
			return nil, err
		}
		if f.completeErr != nil {
			return nil, f.completeErr
		}
		return json.Marshal(completeBootstrapReply{
			InstalledFingerprint: f.installedFP,
			NotAfter:             time.Now().Add(90 * 24 * time.Hour).UTC(),
		})
	}
	return nil, fmt.Errorf("unexpected service %q", service)
}

// fakeIssuer stands in for BootstrapIssuer, whose own ordering is covered in
// bootstrap_test.go.
type fakeIssuer struct {
	issued   *IssuedBootstrapCert
	err      error
	gotToken string
	gotCSR   string
}

func (f *fakeIssuer) IssueFirstCertificate(_ context.Context, token, csrPEM string) (*IssuedBootstrapCert, error) {
	f.gotToken, f.gotCSR = token, csrPEM
	if f.err != nil {
		return nil, f.err
	}
	return f.issued, nil
}

func testBootstrapQube() *models.Qube {
	return &models.Qube{ID: "qube-1", Name: "remote-dev", IPAddress: "10.0.0.9"}
}

func issuedFor(qubeName, fingerprint string) *IssuedBootstrapCert {
	return &IssuedBootstrapCert{
		QubeID:      "qube-1",
		QubeName:    qubeName,
		CertPEM:     "-----BEGIN CERTIFICATE-----\nissued\n-----END CERTIFICATE-----",
		CAPEM:       "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
		Fingerprint: fingerprint,
		SubjectCN:   AgentCommonName(qubeName),
		NotAfter:    time.Now().Add(90 * 24 * time.Hour).UTC(),
	}
}

// runBootstrapExchange drives the protocol against a fake agent, skipping the dial.
func runBootstrapExchange(b *AgentBootstrapper, ag *fakeBootstrapAgent, qube *models.Qube) BootstrapResult {
	started := time.Now()
	res := BootstrapResult{At: started.UTC(), QubeID: qube.ID, QubeName: qube.Name}
	done := func(status BootstrapStatus, format string, args ...any) BootstrapResult {
		res.Status = status
		res.Duration = time.Since(started)
		if format != "" {
			res.Reason = fmt.Sprintf(format, args...)
		}
		return res
	}
	return b.exchange(context.Background(), qube, ag, done)
}

func TestBootstrapExchangeSucceeds(t *testing.T) {
	const fp = "abcdef0123456789abcdef"
	ag := &fakeBootstrapAgent{
		beginReply:  beginBootstrapReply{Nonce: "n1", Token: "tok", CSRPEM: "csr"},
		installedFP: fp,
	}
	iss := &fakeIssuer{issued: issuedFor("remote-dev", fp)}
	b := NewAgentBootstrapper(nil, iss, "", 0)

	res := runBootstrapExchange(b, ag, testBootstrapQube())

	require.Equal(t, BootstrapOK, res.Status, res.Reason)
	assert.Equal(t, fp, res.Fingerprint)
	assert.Equal(t, "tok", iss.gotToken, "the token the agent surrendered must be what gets redeemed")
	assert.Equal(t, "csr", iss.gotCSR)
	assert.Equal(t, "n1", ag.gotComplete.Nonce, "the second call must carry the nonce from the first")
	assert.Contains(t, ag.gotComplete.CertPEM, "issued")
	assert.NotContains(t, ag.gotComplete.CertPEM, "PRIVATE KEY")
}

// An agent that already holds an identity is the expected answer on every
// sweep after the first. Reporting it as a fault would make a converged fleet
// look permanently broken.
func TestBootstrapAgainstAnAlreadyBootstrappedAgent(t *testing.T) {
	ag := &fakeBootstrapAgent{
		// The transport carries the agent's error as a message, which is why
		// the console matches against the agent's own sentinel text.
		beginErr: fmt.Errorf("call failed: %w", agent.ErrAlreadyBootstrapped),
	}
	iss := &fakeIssuer{}
	b := NewAgentBootstrapper(nil, iss, "", 0)

	res := runBootstrapExchange(b, ag, testBootstrapQube())

	assert.Equal(t, BootstrapAlreadyDone, res.Status)
	assert.Empty(t, iss.gotToken, "no token should have been redeemed")
	assert.True(t, res.Status.AgentAnswered(), "the agent demonstrably answered")
}

func TestBootstrapUnreachableAgentIsNotAConsoleFault(t *testing.T) {
	ag := &fakeBootstrapAgent{beginErr: errors.New("connection refused")}
	b := NewAgentBootstrapper(nil, &fakeIssuer{}, "", 0)

	res := runBootstrapExchange(b, ag, testBootstrapQube())

	assert.Equal(t, BootstrapUnreachable, res.Status)
	assert.False(t, res.Status.AgentAnswered())
}

// A rejected token cannot be retried into working — the qube needs new
// user-data. Reporting it as a generic console fault would send an operator to
// debug a CA that is fine.
func TestBootstrapRejectedTokenIsDistinctFromAConsoleFault(t *testing.T) {
	ag := &fakeBootstrapAgent{beginReply: beginBootstrapReply{Nonce: "n1", Token: "stale", CSRPEM: "csr"}}
	iss := &fakeIssuer{err: fmt.Errorf("wrapped: %w", repository.ErrBootstrapTokenRejected)}
	b := NewAgentBootstrapper(nil, iss, "", 0)

	res := runBootstrapExchange(b, ag, testBootstrapQube())

	assert.Equal(t, BootstrapRefused, res.Status)
	assert.Contains(t, res.Reason, "re-provisioning")
	assert.NotContains(t, ag.calls, completeBootstrapService,
		"nothing should have been delivered after a refused token")
}

// The counterfactual that matters most: a token belonging to ANOTHER qube must
// not produce an installed certificate. Two guests provisioned with each
// other's user-data would otherwise each get an identity the prober refuses at
// the very next sweep — a green bootstrap followed by an inexplicable outage.
func TestBootstrapRefusesACertificateForAnotherQube(t *testing.T) {
	ag := &fakeBootstrapAgent{beginReply: beginBootstrapReply{Nonce: "n1", Token: "tok", CSRPEM: "csr"}}
	iss := &fakeIssuer{issued: issuedFor("remote-prod", "aabbccddeeff00112233")}
	b := NewAgentBootstrapper(nil, iss, "", 0)

	res := runBootstrapExchange(b, ag, testBootstrapQube())

	assert.Equal(t, BootstrapConsoleFailed, res.Status)
	assert.Contains(t, res.Reason, "agent-remote-prod")
	assert.NotContains(t, ag.calls, completeBootstrapService,
		"a certificate for the wrong qube was delivered anyway")
}

// The console must confirm what was installed rather than infer it from a call
// that returned without error — otherwise the registry authorizes a
// fingerprint the agent will never present, and the qube locks itself out.
func TestBootstrapRejectsAWrongInstalledFingerprint(t *testing.T) {
	ag := &fakeBootstrapAgent{
		beginReply:  beginBootstrapReply{Nonce: "n1", Token: "tok", CSRPEM: "csr"},
		installedFP: "something-else-entirely",
	}
	iss := &fakeIssuer{issued: issuedFor("remote-dev", "abcdef0123456789abcdef")}
	b := NewAgentBootstrapper(nil, iss, "", 0)

	res := runBootstrapExchange(b, ag, testBootstrapQube())

	assert.Equal(t, BootstrapInstallFailed, res.Status)
	assert.Contains(t, res.Reason, "reports installing")
}

// A lost reply to the second call leaves a spent token and a registered
// certificate the agent may or may not hold. It must be reported as needing a
// fresh token, not as something a retry can fix.
func TestBootstrapInstallFailureAsksForAFreshToken(t *testing.T) {
	ag := &fakeBootstrapAgent{
		beginReply:  beginBootstrapReply{Nonce: "n1", Token: "tok", CSRPEM: "csr"},
		completeErr: errors.New("tunnel dropped"),
	}
	iss := &fakeIssuer{issued: issuedFor("remote-dev", "abcdef0123456789abcdef")}
	b := NewAgentBootstrapper(nil, iss, "", 0)

	res := runBootstrapExchange(b, ag, testBootstrapQube())

	assert.Equal(t, BootstrapInstallFailed, res.Status)
	assert.Contains(t, res.Reason, "fresh token")
}

func TestBootstrapIncompleteBeginReplyIsRefused(t *testing.T) {
	for name, reply := range map[string]beginBootstrapReply{
		"no nonce": {Token: "tok", CSRPEM: "csr"},
		"no token": {Nonce: "n1", CSRPEM: "csr"},
		"no csr":   {Nonce: "n1", Token: "tok"},
	} {
		t.Run(name, func(t *testing.T) {
			ag := &fakeBootstrapAgent{beginReply: reply}
			iss := &fakeIssuer{}
			b := NewAgentBootstrapper(nil, iss, "", 0)

			res := runBootstrapExchange(b, ag, testBootstrapQube())

			assert.Equal(t, BootstrapConsoleFailed, res.Status)
			assert.Empty(t, iss.gotToken, "an incomplete reply must not reach the issuer")
		})
	}
}

// stubCA is enough to get past the "is this console configured" check; the
// address test must fail on the missing address, not on the missing CA.
type stubCA struct{}

func (stubCA) CA(context.Context) (*pki.CA, error) { return pki.NewCA("test-console", 0) }

func TestBootstrapWithoutAnAddressIsUnreachable(t *testing.T) {
	b := NewAgentBootstrapper(stubCA{}, &fakeIssuer{}, "", 0)
	res := b.Bootstrap(context.Background(), &models.Qube{ID: "q", Name: "remote-dev"})

	assert.Equal(t, BootstrapUnreachable, res.Status)
	assert.Contains(t, res.Reason, "no IP address")
}

func TestBootstrapNotConfigured(t *testing.T) {
	var b *AgentBootstrapper
	res := b.Bootstrap(context.Background(), testBootstrapQube())

	assert.Equal(t, BootstrapNotConfigured, res.Status)
	assert.False(t, res.Status.AgentAnswered())
}
