// bootstrap.go — first-certificate issuance against a one-shot token.
//
// This logic began life as an HTTP handler mounted at /bootstrap/certificate,
// which required the AGENT to dial the CONSOLE — a direction nothing else in
// the system uses (docs/bootstrap-design.md §9.3). The transport was removed;
// the logic survived unchanged because none of it was ever about HTTP. The
// ordering below is what makes a one-shot token actually one-shot, whichever
// channel carries the request, and the console-side bootstrapper that dials
// the agent is now the only caller.
//
// One property from the HTTP era dissolved rather than moved: the endpoint
// used to answer an untrusted remote caller, so redemption failures had to be
// opaque. Now the caller is this console's own dialer — the detailed reason
// stays in-process and goes to the log, and the agent simply never receives a
// certificate.
package service

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/slchris/qubes-air/console/internal/repository"
)

// ErrBootstrapCSRInvalid marks a CSR refused BEFORE the token was redeemed.
// The caller's token is still live: a malformed request must not burn a
// credential the agent may still need after it fixes its own bug.
var ErrBootstrapCSRInvalid = errors.New("bootstrap CSR not acceptable")

// bootstrapCertLifetime is how long an issued bootstrap certificate lasts.
// Zero means "use the signer's default"; naming it here keeps the first
// certificate on the same schedule as every renewed one.
const bootstrapCertLifetime = 0

// BootstrapTokenRedeemer consumes a bootstrap token and reports which qube it
// authorizes.
type BootstrapTokenRedeemer interface {
	Redeem(ctx context.Context, secret string, now time.Time) (*repository.BootstrapToken, error)
}

// BootstrapCSRSigner signs an agent-generated CSR for a common name the caller
// has already proven the right to. *CertIssuer satisfies it.
type BootstrapCSRSigner interface {
	SignAgentCSR(ctx context.Context, csrPEM, wantCN string, lifetime time.Duration) (*SignedAgentCert, error)
}

// BootstrapCertRegistrar records an issued certificate as authorized.
type BootstrapCertRegistrar interface {
	Register(ctx context.Context, c *repository.AgentCert) error
}

// BootstrapIssuer issues an agent its first certificate against a one-shot
// token, so the console never has to ship a private key.
//
// Renewal already had the agent generate its own key and send only a CSR;
// bootstrap could not, because renewal is authenticated by the mTLS
// certificate the agent does not yet have. The token breaks that cycle — it
// authorizes issuing exactly one certificate, for exactly one qube, once,
// before it expires.
type BootstrapIssuer struct {
	tokens BootstrapTokenRedeemer
	signer BootstrapCSRSigner
	certs  BootstrapCertRegistrar
}

// NewBootstrapIssuer builds the issuer.
func NewBootstrapIssuer(tokens BootstrapTokenRedeemer, signer BootstrapCSRSigner, certs BootstrapCertRegistrar) *BootstrapIssuer {
	return &BootstrapIssuer{tokens: tokens, signer: signer, certs: certs}
}

// IssuedBootstrapCert is the identity handed back to the agent. It contains no
// private key — the key never left the agent, which is the point of the whole
// exchange.
type IssuedBootstrapCert struct {
	QubeID      string
	QubeName    string
	CertPEM     string
	CAPEM       string
	Fingerprint string
	SubjectCN   string
	NotAfter    time.Time
}

// IssueFirstCertificate redeems a token and signs the CSR it authorizes.
func (b *BootstrapIssuer) IssueFirstCertificate(ctx context.Context, token, csrPEM string) (*IssuedBootstrapCert, error) {
	if b == nil || b.tokens == nil || b.signer == nil || b.certs == nil {
		return nil, errors.New("bootstrap issuer is not configured")
	}

	// The CSR is checked BEFORE the token is redeemed. Redemption is
	// irreversible, so everything that can be judged without the token must be
	// judged first — a CSR that was never going to be signable must not cost
	// the agent its one credential. Only syntax, proof-of-possession and the
	// no-SAN rule can be checked here; the common name needs the redeemed
	// record and is enforced by the signer below.
	if err := checkBootstrapCSR(csrPEM); err != nil {
		return nil, err
	}

	// Redeem BEFORE signing, and accept the cost of that ordering.
	//
	// The other order — sign, then mark the token spent — fails open: a crash
	// or error between the two leaves a certificate issued AND a live token, so
	// a second caller can obtain a second certificate for the same name. That
	// is precisely the impersonation single-use exists to prevent.
	//
	// This order fails closed instead: a signing failure burns the token and
	// the qube cannot bootstrap until a new one is minted. That is a real
	// operational cost, and it is the right side to err on — an operator
	// re-issuing a token is recoverable, two agents sharing an identity is not.
	tok, err := b.tokens.Redeem(ctx, token, time.Now())
	if err != nil {
		log.Printf("bootstrap: refusing certificate request: %v", err)
		return nil, err
	}

	// The common name comes from the REDEEMED RECORD, never from the request
	// or the CSR. The token is the only thing the caller proved; letting the
	// request name the identity would let any valid token mint a certificate
	// for the most valuable qube in the fleet.
	wantCN := AgentCommonName(tok.QubeName)

	signed, err := b.signer.SignAgentCSR(ctx, csrPEM, wantCN, bootstrapCertLifetime)
	if err != nil {
		// The pre-check above cannot see the common name, so a CSR for the
		// wrong identity lands here — after redemption, deliberately: a valid
		// token attached to a CSR for another qube's name is the escalation
		// case, and burning the token on it is the fail-closed answer.
		log.Printf("bootstrap: signing failed for %q (token is now spent): %v", tok.QubeName, err)
		return nil, fmt.Errorf("certificate request for %q was not signable: %w", tok.QubeName, err)
	}

	// Register BEFORE handing the certificate over, for the same reason renewal
	// does: the registry is what authorizes a connection, so returning a
	// certificate that is not in it would give the agent a credential the
	// server refuses — an identity that looks correct everywhere and works
	// nowhere.
	notAfter := signed.NotAfter
	if err := b.certs.Register(ctx, &repository.AgentCert{
		Fingerprint: signed.Fingerprint,
		QubeID:      tok.QubeID,
		SubjectCN:   signed.SubjectCN,
		IssuedAt:    time.Now().UTC(),
		ExpiresAt:   &notAfter,
	}); err != nil {
		log.Printf("bootstrap: registering certificate for %q failed: %v", tok.QubeName, err)
		return nil, fmt.Errorf("could not register the issued certificate for %q: %w", tok.QubeName, err)
	}

	log.Printf("bootstrap: issued certificate %s to %q (CN=%s, expires %s)",
		shortFingerprint(signed.Fingerprint), tok.QubeName, signed.SubjectCN, notAfter.Format(time.RFC3339))

	return &IssuedBootstrapCert{
		QubeID:      tok.QubeID,
		QubeName:    tok.QubeName,
		CertPEM:     signed.CertPEM,
		CAPEM:       signed.CAPEM,
		Fingerprint: signed.Fingerprint,
		SubjectCN:   signed.SubjectCN,
		NotAfter:    signed.NotAfter,
	}, nil
}

// checkBootstrapCSR refuses a CSR that could never be signed, without touching
// the token.
//
// It is verifyRenewalCSR minus the common-name check: the CN can only be
// judged against the redeemed record, which does not exist yet at this point.
// Everything else is the same decision for the same reasons — the signature
// proves the requester holds the private half, and a CSR carrying subject
// alternative names is asking for a certificate shape agents never get.
func checkBootstrapCSR(csrPEM string) error {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return fmt.Errorf("%w: not a PEM certificate request", ErrBootstrapCSRInvalid)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("%w: unparseable certificate request: %v", ErrBootstrapCSRInvalid, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return fmt.Errorf("%w: not signed by the key it asks to certify: %v", ErrBootstrapCSRInvalid, err)
	}
	if n := len(csr.DNSNames) + len(csr.IPAddresses) + len(csr.EmailAddresses) + len(csr.URIs); n > 0 {
		return fmt.Errorf("%w: carries %d subject alternative name(s); agent certificates carry none",
			ErrBootstrapCSRInvalid, n)
	}
	return nil
}
