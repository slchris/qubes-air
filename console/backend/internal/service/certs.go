package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// Credential names under which the CA is stored.
//
// The CA private key is the single most valuable secret this console holds:
// whoever has it can mint any agent identity in the fleet. It lives in the
// encrypted credential store rather than on disk so it is protected by the same
// keyring, and can be rotated by the same machinery.
// gosec G101 fires on both names because they contain "cert"/"key". They are
// the LOOKUP NAMES a secret is stored under, not the secret — the value they
// address never appears in this file.
const (
	caCertCredentialName = "qubes-air-ca-cert" //nolint:gosec // G101: a store key, not a credential
	caKeyCredentialName  = "qubes-air-ca-key"  //nolint:gosec // G101: a store key, not a credential
	caCredentialType     = "pki"
)

// CertIssuer issues and registers agent certificates.
//
// Issuance and registration are deliberately coupled: a certificate that is
// signed but not registered would be refused at connection time, and one that
// is registered without being issued cannot exist. Doing them together is what
// keeps the CA and the revocation registry describing the same reality.
type CertIssuer struct {
	creds CredentialStore
	certs *repository.AgentCertRepository
	// identityDir is where rendered cloud-init identity files are written.
	// Empty disables delivery: certificates are still issued and registered,
	// but never reach the remote.
	//
	// Its meaning depends on snippetDatastore: with it empty, this is a private
	// staging directory terraform uploads FROM over SFTP; with it set, this is
	// a mount of the shared storage the PVE nodes read snippets from directly.
	identityDir string
	// snippetDatastore names the Proxmox datastore backing identityDir, and
	// switching it on is what removes node SSH from the provisioning path.
	//
	// Empty keeps the SFTP path, which is the one proven on real hardware. The
	// two are kept side by side deliberately: shared storage changes how every
	// qube is provisioned, and a mode that cannot be turned back off would make
	// a bad night unrecoverable.
	snippetDatastore string
	// agentListen is the address the agent binds on the remote.
	agentListen string
	// agentPkg pins the .deb that installs the agent binary on the remote.
	// Unset means the rendered document has no package to install, and says so
	// loudly in the guest rather than producing a qube with a dead unit.
	agentPkg AgentPackage

	// tokens mints the one-shot bootstrap credentials cloud-init delivers.
	// Without it a qube can be created but has no way to ever obtain an
	// identity, so IssueFor refuses rather than rendering a document that
	// cannot work.
	tokens BootstrapTokenStore
	// bootstrapTokenTTL overrides defaultBootstrapTokenTTL. Zero takes the
	// default.
	bootstrapTokenTTL time.Duration

	// mu guards lazy CA initialization so two concurrent qube creations cannot
	// each mint a CA and race to store it — which would leave agents trusting
	// different roots.
	mu sync.Mutex
	ca *pki.CA
}

// BootstrapTokenStore mints and invalidates bootstrap tokens.
// Implemented by *repository.BootstrapTokenRepository.
type BootstrapTokenStore interface {
	Issue(ctx context.Context, qubeID, qubeName string, ttl time.Duration) (string, error)
	InvalidateForQube(ctx context.Context, qubeID string, now time.Time) (int64, error)
}

// CredentialStore is the subset of the credential repository this needs.
type CredentialStore interface {
	GetSecret(ctx context.Context, id string) (string, error)
	List(ctx context.Context) ([]models.Credential, error)
	Create(ctx context.Context, req models.CredentialCreateRequest) (*models.Credential, error)
}

// NewCertIssuer builds an issuer over the credential store and registry.
func NewCertIssuer(creds CredentialStore, certs *repository.AgentCertRepository, identityDir, agentListen string, agentPkg AgentPackage) *CertIssuer {
	return &CertIssuer{
		creds:       creds,
		certs:       certs,
		identityDir: identityDir,
		agentListen: agentListen,
		agentPkg:    agentPkg,
	}
}

// WithSnippetDatastore switches identity delivery onto shared storage backed by
// the named Proxmox datastore, taking node SSH off the provisioning path.
//
// An option rather than a constructor argument so that every existing caller
// keeps the SFTP behavior it has today: this changes how every qube is
// provisioned, and it should be something a deployment opts into, not something
// a forgotten empty string decides.
func (c *CertIssuer) WithSnippetDatastore(datastore string) *CertIssuer {
	c.snippetDatastore = strings.TrimSpace(datastore)
	return c
}

// WithBootstrapTokens supplies the token store IssueFor mints from, and
// optionally overrides the token lifetime (zero keeps the default).
func (c *CertIssuer) WithBootstrapTokens(tokens BootstrapTokenStore, ttl time.Duration) *CertIssuer {
	c.tokens = tokens
	c.bootstrapTokenTTL = ttl
	return c
}

// IdentityPath returns where a qube's rendered identity file lives, or "" when
// delivery is not configured or runs over shared storage.
//
// Empty in shared-storage mode on purpose: this path exists so terraform can
// UPLOAD the file over SFTP, and in that mode terraform must not upload
// anything — the file is already where the nodes read it. Returning a path
// there would make terraform re-upload it into node-local storage and
// reintroduce the SSH requirement the mode exists to remove.
func (c *CertIssuer) IdentityPath(qubeName string) string {
	if c.identityDir == "" || c.snippetDatastore != "" {
		return ""
	}
	return filepath.Join(c.identityDir, SnippetFileName(qubeName))
}

// IdentityVolumeID returns the Proxmox volume id of a qube's identity snippet,
// or "" when delivery is not over shared storage or no identity exists.
//
// Read from the SHARE rather than remembered in the process, and that is the
// load-bearing choice. The file name carries a hash of its own content
// (ContentAddressedSnippetName), so it cannot be recomputed from the qube name
// — which makes an in-memory map tempting, and wrong: a console restart would
// lose every name, tfvars would render qubes with no identity, and terraform
// would happily rebuild running VMs without one. The share is where the file
// actually is, so the share is what gets asked.
func (c *CertIssuer) IdentityVolumeID(qubeName string) string {
	if c.snippetDatastore == "" || c.identityDir == "" {
		return ""
	}
	name, err := FindSharedAgentUserData(c.identityDir, qubeName)
	if err != nil {
		// Reported, not guessed. A fabricated volume id is a cicustom entry PVE
		// cannot resolve, and the VM then fails to start with an error three
		// layers from the cause; an empty one is visibly "no identity yet".
		log.Printf("pki: cannot determine the identity snippet for %q on the share: %v", qubeName, err)
		return ""
	}
	if name == "" {
		return ""
	}
	return SnippetVolumeID(c.snippetDatastore, name)
}

// IssueFor mints a qube's bootstrap credential and renders its delivery
// document.
//
// It no longer issues a CERTIFICATE, and that is the change the whole bootstrap
// design exists for. Cloud-init used to carry the agent's private key, which
// then existed in at least three places it did not need to: the rendered file
// on the console, the snippet on the hypervisor, and any backup of either. On
// Proxmox that made VM.Config.Cloudinit equivalent to holding every agent
// identity ever delivered.
//
// Now the guest is given a public CA and a one-shot token. The agent generates
// its own key at first boot, and the console dials in and signs a CSR against
// that token (service.AgentBootstrapper). So a qube is created with NO
// certificate, deliberately, and BootstrapMonitor is what turns the token into
// one — a console with that sweep disabled will provision qubes that never
// obtain an identity, which is why it says so loudly at startup.
//
// Any outstanding token is invalidated first. A qube being re-provisioned must
// not remain claimable with a token minted for an earlier attempt, and the
// order matters: mint-then-invalidate would spend the token it just created.
func (c *CertIssuer) IssueFor(ctx context.Context, qube *models.Qube) error {
	if c.tokens == nil {
		return fmt.Errorf("no bootstrap token store configured; qube %q cannot be given a way to obtain an identity", qube.Name)
	}
	ca, err := c.loadOrCreateCA(ctx)
	if err != nil {
		return fmt.Errorf("certificate authority: %w", err)
	}

	if n, err := c.tokens.InvalidateForQube(ctx, qube.ID, time.Now()); err != nil {
		// Fatal, unlike most cleanup. A surviving old token is a second way to
		// claim this qube's name, which is exactly what single-use exists to
		// prevent.
		return fmt.Errorf("invalidate previous bootstrap tokens for %q: %w", qube.Name, err)
	} else if n > 0 {
		log.Printf("pki: invalidated %d outstanding bootstrap token(s) for %q before minting a new one", n, qube.Name)
	}

	token, err := c.tokens.Issue(ctx, qube.ID, qube.Name, c.tokenTTL())
	if err != nil {
		return fmt.Errorf("mint bootstrap token for %q: %w", qube.Name, err)
	}

	// Render and persist the delivery document. Without this the token exists
	// but has no way to reach the remote, which is exactly the state this whole
	// chain existed to leave behind.
	if c.identityDir != "" {
		userData, err := RenderAgentUserData(qube.Name, AgentIdentityDoc{
			CAPEM:          pki.EncodeCACertPEM(ca),
			BootstrapToken: token,
		}, c.agentListen, c.agentPkg)
		if err != nil {
			return fmt.Errorf("render identity for %q: %w", qube.Name, err)
		}
		if c.snippetDatastore != "" {
			// Shared storage: the console writes where the nodes already read,
			// so terraform uploads nothing and needs no SSH to a hypervisor.
			name, err := WriteSharedAgentUserData(c.identityDir, qube.Name, userData)
			if err != nil {
				return fmt.Errorf("persist identity for %q on the share: %w", qube.Name, err)
			}
			log.Printf("pki: wrote agent identity for %q to %s",
				qube.Name, SnippetVolumeID(c.snippetDatastore, name))
		} else {
			path, err := WriteAgentUserData(c.identityDir, qube.Name, userData)
			if err != nil {
				return fmt.Errorf("persist identity for %q: %w", qube.Name, err)
			}
			log.Printf("pki: wrote agent identity for %q to %s", qube.Name, path)
		}
	}

	log.Printf("pki: qube %q can now bootstrap; its certificate is issued when the console reaches its agent", qube.Name)
	return nil
}

// defaultBootstrapTokenTTL is how long a minted token stays redeemable.
//
// Sized against MEASURED provisioning, not intuition. The window that has to
// fit inside it is "terraform starts the apply" through "the agent is up and
// the console has dialed it", and section 7.4 records a provision spending 14
// minutes in apt alone before that was fixed. A token that felt short —
// five minutes, say — would expire during one slow boot, and the failure only
// becomes visible when the console dials and is refused, long after the apply
// reported success.
//
// An hour is far past any provision observed here while still bounding a leak
// to a window, which is the property being bought.
const defaultBootstrapTokenTTL = time.Hour

func (c *CertIssuer) tokenTTL() time.Duration {
	if c.bootstrapTokenTTL > 0 {
		return c.bootstrapTokenTTL
	}
	return defaultBootstrapTokenTTL
}

// ReissueFor replaces a qube's agent identity, retiring whatever it held before.
//
// This is the resume path, and it exists because a suspended qube CANNOT renew.
// Suspend DESTROYS the compute instance and keeps only the data disk (see
// terraform/modules/remote-qube-base: compute_running=false), so there is no
// agent process to renew against — the renewal sweep skips suspended qubes for
// exactly that reason. A qube suspended across its whole renewal window is
// therefore resumed with an EXPIRED certificate, and the agent deliberately
// refuses to start without a valid one rather than serve its qrexec services to
// anyone on the LAN. Nothing about that qube looks wrong until someone tries to
// use it, and by then it is locked out with no way back in over the mTLS
// channel that would have fixed it.
//
// Resume is where that closes, and closing it there costs nothing: resume
// already destroys and rebuilds the compute instance, so a fresh identity rides
// along on a cloud-init document terraform was going to render and upload
// anyway. It also means a resumed qube always starts from a full certificate
// lifetime instead of whatever was left of an old one.
//
// Revocation happens BEFORE issuance, never after. RevokeByQube revokes every
// row belonging to the qube, so running it afterwards would revoke the
// certificate just minted and the qube would boot holding a revoked one — the
// same permanent lockout, reached from the other side.
func (c *CertIssuer) ReissueFor(ctx context.Context, qube *models.Qube, reason string) error {
	if qube == nil {
		return errors.New("reissue: no qube given")
	}

	// The instance that held the previous certificate is gone: suspend destroyed
	// it along with its OS disk. Leaving that certificate valid for the rest of
	// its 90 days would leave a credential with no legitimate holder — usable by
	// anyone who kept a copy of the cloud-init snippet, the OS disk image or a
	// backup of either. Renewal deliberately does not revoke, because there the
	// agent is still running and still holding the old certificate; here there is
	// no agent and nothing to protect.
	if err := c.RevokeFor(ctx, qube.ID, reason); err != nil {
		// Not fatal, on purpose. Refusing to resume because an OLD certificate
		// could not be retired would convert a bookkeeping failure into exactly
		// the lockout this function exists to prevent. Loud, because until it is
		// fixed that certificate keeps authenticating until it expires on its own.
		log.Printf("pki: could NOT revoke the previous certificate(s) for qube %q (%v); "+
			"reissuing anyway — the old certificate stays valid until it expires", qube.Name, err)
	}

	return c.IssueFor(ctx, qube)
}

// RevokeFor revokes every certificate issued to a qube.
//
// Called when a qube is purged: a decommissioned machine must not keep a
// working credential.
func (c *CertIssuer) RevokeFor(ctx context.Context, qubeID, reason string) error {
	n, err := c.certs.RevokeByQube(ctx, qubeID, reason)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("pki: revoked %d certificate(s) for qube %s: %s", n, qubeID, reason)
	}
	return nil
}

// loadOrCreateCA returns the console's CA, creating and storing it on first use.
func (c *CertIssuer) loadOrCreateCA(ctx context.Context) (*pki.CA, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ca != nil {
		return c.ca, nil
	}

	certPEM, certErr := c.findCredential(ctx, caCertCredentialName)
	keyPEM, keyErr := c.findCredential(ctx, caKeyCredentialName)

	switch {
	case certErr == nil && keyErr == nil:
		ca, err := pki.ParseCA(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("stored CA is unusable: %w", err)
		}
		c.ca = ca
		return ca, nil

	case errors.Is(certErr, errCredentialNotFound) && errors.Is(keyErr, errCredentialNotFound):
		return c.createCA(ctx)

	default:
		// Exactly one half present means a partial write or a partial deletion.
		// Minting a replacement would silently invalidate every certificate the
		// missing half signed, so refuse and let a human look.
		return nil, fmt.Errorf(
			"CA is half-present (cert: %v, key: %v); refusing to mint a replacement, "+
				"which would invalidate every already-issued certificate", certErr, keyErr)
	}
}

// createCA mints and stores a new CA.
func (c *CertIssuer) createCA(ctx context.Context) (*pki.CA, error) {
	ca, err := pki.NewCA("qubes-air-console", pki.DefaultCALifetime)
	if err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := ca.MarshalCA()
	if err != nil {
		return nil, err
	}

	// Store the certificate first. If the process dies between the two writes
	// the result is a half-present CA, which loadOrCreateCA refuses rather than
	// papering over — that is the intended behavior, not an oversight.
	if _, err := c.creds.Create(ctx, models.CredentialCreateRequest{
		Name:        caCertCredentialName,
		Type:        caCredentialType,
		Description: "Qubes Air agent CA certificate (public)",
		SecretValue: certPEM,
	}); err != nil {
		return nil, fmt.Errorf("store CA certificate: %w", err)
	}
	if _, err := c.creds.Create(ctx, models.CredentialCreateRequest{
		Name:        caKeyCredentialName,
		Type:        caCredentialType,
		Description: "Qubes Air agent CA PRIVATE KEY — whoever holds this can mint any agent identity",
		SecretValue: keyPEM,
	}); err != nil {
		return nil, fmt.Errorf("store CA key: %w", err)
	}

	log.Printf("pki: created a new agent CA (valid until %s)", ca.Cert.NotAfter.Format(time.RFC3339))
	c.ca = ca
	return ca, nil
}

// errCredentialNotFound distinguishes "absent" from "broken" when loading.
var errCredentialNotFound = errors.New("credential not found")

// findCredential looks a credential up by name and returns its secret.
func (c *CertIssuer) findCredential(ctx context.Context, name string) (string, error) {
	list, err := c.creds.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list credentials: %w", err)
	}
	for _, cred := range list {
		if strings.EqualFold(cred.Name, name) {
			return c.creds.GetSecret(ctx, cred.ID)
		}
	}
	return "", errCredentialNotFound
}
