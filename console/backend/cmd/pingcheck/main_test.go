package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
)

func newTestCA(t *testing.T) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA("qubes-air-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	return ca
}

// agentLeaf is the certificate an agent named cn, issued by ca, presents.
func agentLeaf(t *testing.T, ca *pki.CA, cn string) *x509.Certificate {
	t.Helper()
	b, err := ca.IssueAgentCert(cn, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentCert(%q): %v", cn, err)
	}
	pair, err := tls.X509KeyPair([]byte(b.CertPEM), []byte(b.KeyPEM))
	if err != nil {
		t.Fatalf("load issued pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse issued leaf: %v", err)
	}
	return leaf
}

// dialConfig is the config pingcheck dials "target" with: a client certificate
// from ca, trusting ca only, with the verified agent reported to out.
func dialConfig(t *testing.T, ca *pki.CA, out *bytes.Buffer) *tls.Config {
	t.Helper()
	b, err := ca.IssueAgentCert("pingcheck-client", time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	pair, err := tls.X509KeyPair([]byte(b.CertPEM), []byte(b.KeyPEM))
	if err != nil {
		t.Fatalf("client key pair: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	cfg, err := pki.AgentDialTLSConfig(pair, roots, "target")
	if err != nil {
		t.Fatalf("AgentDialTLSConfig: %v", err)
	}
	reportVerifiedAgent(cfg, out)
	return cfg
}

// TestReportVerifiedAgent_PrintsOnlyAcceptedAgents — the CN line tells the
// operator which agent answered, so it must follow the verdict: the right agent
// is named, and a refused peer is refused as before and never printed.
func TestReportVerifiedAgent_PrintsOnlyAcceptedAgents(t *testing.T) {
	ca := newTestCA(t)

	t.Run("the agent it dialed", func(t *testing.T) {
		var out bytes.Buffer
		cfg := dialConfig(t, ca, &out)
		cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{agentLeaf(t, ca, pki.AgentCommonName("target"))}}
		if err := cfg.VerifyConnection(cs); err != nil {
			t.Fatalf("the right agent was refused: %v", err)
		}
		if !strings.Contains(out.String(), "CN=agent-target") {
			t.Fatalf("verified agent not reported; printed %q", out.String())
		}
	})

	refused := []struct {
		name string
		peer []*x509.Certificate
		want string
	}{
		{"wrong CA", []*x509.Certificate{agentLeaf(t, newTestCA(t), pki.AgentCommonName("target"))},
			"signed by unknown authority"},
		{"wrong target", []*x509.Certificate{agentLeaf(t, ca, pki.AgentCommonName("other"))},
			`should be serving "agent-target"`},
		{"no certificate", nil, "presented no certificate"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg := dialConfig(t, ca, &out)
			err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: tc.peer})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a refusal saying %q", err, tc.want)
			}
			if out.Len() != 0 {
				t.Fatalf("a refused peer was reported as the agent: %q", out.String())
			}
		})
	}
}

// TestReportVerifiedAgent_FailsClosedWithoutVerifier — wrapping a config that
// has no VerifyConnection must not turn "print on success" into "accept and
// print everything".
func TestReportVerifiedAgent_FailsClosedWithoutVerifier(t *testing.T) {
	ca := newTestCA(t)
	var out bytes.Buffer
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	reportVerifiedAgent(cfg, &out)

	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{agentLeaf(t, ca, pki.AgentCommonName("target"))}}
	if err := cfg.VerifyConnection(cs); err == nil {
		t.Fatal("a config with no verifier accepted the peer")
	}
	if out.Len() != 0 {
		t.Fatalf("an unverified peer was reported as the agent: %q", out.String())
	}
}
