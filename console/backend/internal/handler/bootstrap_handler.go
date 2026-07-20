package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
)

// maxBootstrapBody bounds the request. A CSR for a P-256 key is a few hundred
// bytes; 16 KiB is generous. The endpoint is unauthenticated by design, so it
// must not let an anonymous caller hand us an arbitrary amount of data to parse.
const maxBootstrapBody = 16 << 10

// bootstrapCertLifetime is how long an issued bootstrap certificate lasts.
// Zero would mean "use the signer's default"; naming it here keeps the first
// certificate on the same schedule as every renewed one.
const bootstrapCertLifetime = 0

// TokenRedeemer consumes a bootstrap token and reports which qube it authorizes.
type TokenRedeemer interface {
	Redeem(ctx context.Context, secret string, now time.Time) (*repository.BootstrapToken, error)
}

// CSRSigner signs an agent-generated CSR for a common name the caller has
// already proven the right to.
type CSRSigner interface {
	SignAgentCSR(ctx context.Context, csrPEM, wantCN string, lifetime time.Duration) (*service.SignedAgentCert, error)
}

// CertRegistrar records an issued certificate as authorized.
type CertRegistrar interface {
	Register(ctx context.Context, c *repository.AgentCert) error
}

// BootstrapHandler issues an agent its first certificate against a one-shot
// token, so the console never has to ship a private key.
//
// This is the endpoint that closes the last hole in the delivery path. Renewal
// already had the agent generate its own key and send only a CSR; bootstrap
// could not, because renewal is authenticated by the mTLS certificate the agent
// does not yet have. The token breaks that cycle — it authorizes issuing exactly
// one certificate, for exactly one qube, once, before it expires.
type BootstrapHandler struct {
	tokens TokenRedeemer
	signer CSRSigner
	certs  CertRegistrar
}

// NewBootstrapHandler builds the handler.
func NewBootstrapHandler(tokens TokenRedeemer, signer CSRSigner, certs CertRegistrar) *BootstrapHandler {
	return &BootstrapHandler{tokens: tokens, signer: signer, certs: certs}
}

// RegisterRoutes mounts the endpoint.
//
// The caller MUST mount this outside the authenticated /api/v1 group: an agent
// that has not bootstrapped has no API token, and requiring one would make the
// endpoint unreachable by the only party meant to use it. The bootstrap token
// carried in the body IS the authentication.
func (h *BootstrapHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/certificate", h.IssueCertificate)
}

// BootstrapCertRequest is what a first-booting agent sends.
type BootstrapCertRequest struct {
	// Token is the one-shot secret delivered by cloud-init.
	Token string `json:"token"`
	// CSRPEM is generated on the agent. The private key behind it never leaves
	// the machine — that is the entire point of this endpoint.
	CSRPEM string `json:"csr_pem"`
}

// BootstrapCertResponse is the issued identity. It contains no private key.
type BootstrapCertResponse struct {
	CertPEM     string    `json:"cert_pem"`
	CAPEM       string    `json:"ca_pem"`
	Fingerprint string    `json:"fingerprint"`
	NotAfter    time.Time `json:"not_after"`
	SubjectCN   string    `json:"subject_cn"`
}

// IssueCertificate redeems a token and signs the CSR it authorizes.
func (h *BootstrapHandler) IssueCertificate(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBootstrapBody)

	var req BootstrapCertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: http.StatusText(http.StatusBadRequest),
			// The body is not a secret, so saying what was wrong with it is safe
			// and saves an operator from guessing.
			Message: "malformed bootstrap request: " + err.Error(),
			Code:    http.StatusBadRequest,
		})
		return
	}
	if req.CSRPEM == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   http.StatusText(http.StatusBadRequest),
			Message: "csr_pem is required",
			Code:    http.StatusBadRequest,
		})
		return
	}

	ctx := c.Request.Context()

	// Redeem BEFORE signing, and accept the cost of that ordering.
	//
	// The other order — sign, then mark the token spent — fails open: a crash or
	// error between the two leaves a certificate issued AND a live token, so a
	// second caller can obtain a second certificate for the same name. That is
	// precisely the impersonation single-use exists to prevent.
	//
	// This order fails closed instead: a signing failure burns the token and the
	// qube cannot bootstrap until a new one is minted. That is a real operational
	// cost, and it is the right side to err on — an operator re-issuing a token
	// is recoverable, two agents sharing an identity is not.
	tok, err := h.tokens.Redeem(ctx, req.Token, time.Now())
	if err != nil {
		// The precise reason goes to the log; the caller gets one opaque answer
		// whether the token was unknown, expired or already spent. Telling an
		// attacker which would tell them what to try next.
		log.Printf("bootstrap: refusing certificate request: %v", err)
		if errors.Is(err, repository.ErrBootstrapTokenRejected) {
			c.JSON(http.StatusUnauthorized, ErrorResponse{
				Error:   http.StatusText(http.StatusUnauthorized),
				Message: "bootstrap token not accepted",
				Code:    http.StatusUnauthorized,
			})
			return
		}
		respondError(c, http.StatusInternalServerError, errors.New("bootstrap failed"))
		return
	}

	// The common name comes from the REDEEMED RECORD, never from the request or
	// the CSR. The token is the only thing the caller proved; letting the body
	// name the identity would let any valid token mint a certificate for the
	// most valuable qube in the fleet.
	wantCN := service.AgentCommonName(tok.QubeName)

	signed, err := h.signer.SignAgentCSR(ctx, req.CSRPEM, wantCN, bootstrapCertLifetime)
	if err != nil {
		// A CSR that does not match is the agent's fault, not the console's, and
		// it is worth saying so — but only after the token check has passed, so
		// this cannot be used to probe tokens.
		log.Printf("bootstrap: signing failed for %q (token is now spent): %v", tok.QubeName, err)
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   http.StatusText(http.StatusBadRequest),
			Message: "certificate request was not signable: " + err.Error(),
			Code:    http.StatusBadRequest,
		})
		return
	}

	// Register BEFORE handing the certificate over, for the same reason renewal
	// does: the registry is what authorizes a connection, so returning a
	// certificate that is not in it would give the agent a credential the server
	// refuses — an identity that looks correct everywhere and works nowhere.
	notAfter := signed.NotAfter
	if err := h.certs.Register(ctx, &repository.AgentCert{
		Fingerprint: signed.Fingerprint,
		QubeID:      tok.QubeID,
		SubjectCN:   signed.SubjectCN,
		IssuedAt:    time.Now().UTC(),
		ExpiresAt:   &notAfter,
	}); err != nil {
		log.Printf("bootstrap: registering certificate for %q failed: %v", tok.QubeName, err)
		respondError(c, http.StatusInternalServerError, errors.New("could not register the issued certificate"))
		return
	}

	log.Printf("bootstrap: issued certificate %s to %q (CN=%s, expires %s)",
		signed.Fingerprint[:16], tok.QubeName, signed.SubjectCN, notAfter.Format(time.RFC3339))

	c.JSON(http.StatusOK, BootstrapCertResponse{
		CertPEM:     signed.CertPEM,
		CAPEM:       signed.CAPEM,
		Fingerprint: signed.Fingerprint,
		NotAfter:    signed.NotAfter,
		SubjectCN:   signed.SubjectCN,
	})
}
