package pki

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// VerifyAgentChain is the whole of a client's trust decision about the agent it
// dialed: the console's probe, renewal and unlock paths, and the relay-call and
// pingcheck tools, all decide "is this the agent I meant" here and nowhere else.
//
// A client dials an agent at whatever address DHCP handed the VM, and the
// agent's certificate is issued per qube name with no SAN for that address, so
// the TLS stack's hostname check cannot run. This replaces it rather than
// weakening it, and must therefore reject everything else on its own:
//
//   - the leaf must chain to roots — this CA and only this CA — be inside its
//     validity window, and carry ServerAuth usage;
//   - the leaf must carry exactly one role, and that role must be RoleAgent;
//   - the leaf's common name must be wantCN, the identity of the qube dialed.
//
// certs is the peer's chain as presented, leaf first. The rest is offered only
// as intermediates; the CA is issued with MaxPathLen 0, so no leaf can extend it.
func VerifyAgentChain(roots *x509.CertPool, certs []*x509.Certificate, wantCN string) error {
	// A nil pool is not "trust nothing": x509.Verify falls back to the system
	// roots, which would let any public CA vouch for an agent.
	if roots == nil {
		return errors.New("pki: no CA pool to verify the agent against")
	}
	if wantCN == "" {
		return errors.New("pki: no agent identity to verify the peer against")
	}
	if len(certs) == 0 {
		return errors.New("agent presented no certificate")
	}
	inters := x509.NewCertPool()
	for _, c := range certs[1:] {
		inters.AddCert(c)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inters,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("agent certificate is not a CA-signed server identity: %w", err)
	}

	// The role says what the holder is FOR. An agent certificate must be the
	// agent's server identity; a Relay/Console client certificate that happens
	// to answer on this address is refused rather than accepted as an agent.
	if role, err := RoleOf(certs[0]); err != nil {
		return fmt.Errorf("agent certificate carries no usable role: %w", err)
	} else if role != RoleAgent {
		return fmt.Errorf("peer role %q is not an agent", role)
	}

	// Chain-to-CA answers "is this OUR fleet"; it does NOT answer "is this the
	// qube we dialed". Every qube holds a CA-signed certificate, so without
	// this check any one of them authenticates as any other.
	//
	// That gap is reachable, not theoretical: qubes share an L2 bridge, so a
	// compromised qube can ARP-spoof another's address (or claim it after a DHCP
	// lease churns) and answer with its OWN valid certificate.
	if got := certs[0].Subject.CommonName; got != wantCN {
		return fmt.Errorf(
			"agent certificate identifies %q but this address should be serving %q; "+
				"a valid fleet certificate presented by the wrong qube", got, wantCN)
	}
	return nil
}

// AgentDialTLSConfig returns the mTLS client configuration for dialing the agent
// of the qube named remoteName, authenticating with client and trusting only
// roots.
//
// It is for callers that hold their own client certificate — the relay-call and
// pingcheck tools, which mint one from the CA or load a provisioned one from
// disk. The console's probe, renewal and unlock paths build theirs in
// service.probeTLSConfig and decide the same question with VerifyAgentChain.
//
// Invalid input is refused rather than defaulted: without roots the chain would
// be checked against the system store, and without a name the pin would be
// "agent-", which no real qube is.
func AgentDialTLSConfig(client tls.Certificate, roots *x509.CertPool, remoteName string) (*tls.Config, error) {
	if len(client.Certificate) == 0 || client.PrivateKey == nil {
		return nil, errors.New("pki: no client certificate to authenticate to the agent with")
	}
	if roots == nil {
		return nil, errors.New("pki: no CA pool to verify the agent against")
	}
	if remoteName == "" {
		return nil, errors.New("pki: no remote name to pin the agent certificate to")
	}
	wantCN := AgentCommonName(remoteName)

	return &tls.Config{
		Certificates: []tls.Certificate{client},
		RootCAs:      roots,
		MinVersion:   tls.VersionTLS13,
		// Hostname verification is replaced, NOT weakened: the agent's
		// certificate carries no SAN for the address dialed, so the stack's own
		// check would reject a good agent. VerifyConnection runs VerifyAgentChain
		// in its place, against roots only.
		InsecureSkipVerify: true, // #nosec G402 -- VerifyConnection below runs VerifyAgentChain on every handshake: CA chain and validity (ServerAuth usage), RoleOf == RoleAgent, and the leaf CN pinned to AgentCommonName(remoteName) //nolint:gosec // chain verified in VerifyConnection
		// VerifyConnection, not VerifyPeerCertificate: the latter is skipped on
		// a resumed session, so a check that lives there can be bypassed by a
		// peer that resumes a cached one. PeerCertificates rather than
		// VerifiedChains, because InsecureSkipVerify leaves the chain unverified
		// by the stack — verifying it is this callback's job.
		VerifyConnection: func(cs tls.ConnectionState) error {
			return VerifyAgentChain(roots, cs.PeerCertificates, wantCN)
		},
	}, nil
}
