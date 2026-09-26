package main

import (
	"crypto/x509"
	"testing"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// The smoke must complete against the transport as it is now. When the server
// began requiring a relay/console role on client certificates, this command
// kept presenting a role-less one and every run failed with "tunnel not
// connected" — nothing executed it, so nothing noticed.
func TestRunCompletesLocalRoundTrip(t *testing.T) {
	if err := run("127.0.0.1:0"); err != nil {
		t.Fatalf("local gRPC smoke round trip failed: %v", err)
	}
}

func TestRunReportsUnusableListenAddress(t *testing.T) {
	if err := run("not-an-address"); err == nil {
		t.Fatal("run accepted a listen address it cannot bind")
	}
}

// Each side must carry exactly the role the production PKI gives it: the
// server is an agent, the caller is a relay. A relay-role server or an
// agent-role caller would make the smoke pass for the wrong reason.
func TestSmokeIdentitiesCarryProductionRoles(t *testing.T) {
	caCert, caKey := mustCA()
	cases := []struct {
		name   string
		server bool
		want   pki.Role
		usage  x509.ExtKeyUsage
	}{
		{name: "server", server: true, want: pki.RoleAgent, usage: x509.ExtKeyUsageServerAuth},
		{name: "client", server: false, want: pki.RoleRelay, usage: x509.ExtKeyUsageClientAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pair := mustLeaf(caCert, caKey, "smoke-"+tc.name, tc.server)
			leaf, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				t.Fatalf("parse leaf: %v", err)
			}
			role, err := pki.RoleOf(leaf)
			if err != nil {
				t.Fatalf("leaf carries no usable role: %v", err)
			}
			if role != tc.want {
				t.Fatalf("role = %q, want %q", role, tc.want)
			}
			if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != tc.usage {
				t.Fatalf("ExtKeyUsage = %v, want [%v]", leaf.ExtKeyUsage, tc.usage)
			}
		})
	}
}
