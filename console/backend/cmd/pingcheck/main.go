// Command pingcheck proves the agent chain end to end against a real qube.
//
// It mints a client certificate from the console's own CA, dials a running
// agent over mTLS, and calls qubesair.Ping. A successful reply means every link
// held: certificate issuance, cloud-init delivery, package download and hash
// verification, install, unit start, mTLS handshake, and qrexec dispatch.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

func main() {
	dsn := flag.String("db", "", "console sqlite DSN")
	// Read from the environment, never a flag: command-line arguments are
	// world-readable through /proc on the same host.
	encKey := os.Getenv("QUBES_AIR_ENCRYPTION_KEY")
	addr := flag.String("addr", "", "agent host:port")
	remote := flag.String("remote", "", "remote name the agent reports")
	flag.Parse()

	// Checked before the database is opened: this needs no connection, and
	// bailing out afterwards would skip the deferred Close.
	if encKey == "" {
		log.Fatal("  ✗ 需要 QUBES_AIR_ENCRYPTION_KEY")
	}

	db, err := database.New(&database.Config{DSN: *dsn})
	must(err)
	defer db.Close()

	kr, err := keyring.NewSingle([]byte(encKey))
	must(err)
	creds := repository.NewCredentialRepository(db, kr)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	certPEM := secretNamed(ctx, creds, "qubes-air-ca-cert")
	keyPEM := secretNamed(ctx, creds, "qubes-air-ca-key")

	ca, err := pki.ParseCA(certPEM, keyPEM)
	must(err)
	fmt.Printf("  CA          : %s\n", ca.Cert.Subject.CommonName)

	// A relay authenticates with a certificate from the same CA that signed the
	// agent's. Minting one here is exactly what the console does for a relay.
	bundle, err := ca.IssueAgentCert("pingcheck-client", time.Hour)
	must(err)
	fmt.Printf("  客户端证书  : %s\n", bundle.Fingerprint[:16])

	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	must(err)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(bundle.CAPEM)) {
		// gocritic: the deferred cancel() is skipped. Accepted — the process is
		// exiting, and the context it would cancel dies with it.
		//nolint:gocritic // exitAfterDefer: the deferred work is moot at exit
		log.Fatal("  ✗ CA 无法解析")
	}

	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: *addr,
		RelayName:      "pingcheck",
		RemoteName:     *remote,
		TLS: &tls.Config{
			Certificates: []tls.Certificate{pair},
			RootCAs:      pool,
			MinVersion:   tls.VersionTLS13,
			// The agent's certificate carries no SAN for this address, so verify
			// the chain by hand rather than skipping verification outright.
			InsecureSkipVerify: true, //nolint:gosec // chain checked in VerifyConnection
			// VerifyConnection, not VerifyPeerCertificate: the latter is skipped
			// on a resumed session, so a check that lives there can be bypassed
			// by a client that reconnects with a cached ticket. PeerCertificates
			// rather than VerifiedChains because InsecureSkipVerify leaves the
			// chain unverified by the stack — verifying it is this callback's job.
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("agent presented no certificate")
				}
				leaf := cs.PeerCertificates[0]
				_, err := leaf.Verify(x509.VerifyOptions{Roots: pool,
					KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
				if err == nil {
					fmt.Printf("  agent 证书  : CN=%s (由本 CA 签发 ✓)\n", leaf.Subject.CommonName)
				}
				return err
			},
		},
	}, nil)

	go func() { _ = cli.Start(ctx) }()

	var out []byte
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		out, err = cli.Call(ctx, *remote, "qubesair.Ping", nil)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		log.Fatalf("  ✗ Ping 失败: %v", err)
	}
	fmt.Printf("  ✓ qubesair.Ping -> %q\n", string(out))
}

func secretNamed(ctx context.Context, r *repository.CredentialRepository, name string) string {
	list, err := r.List(ctx)
	must(err)
	for _, c := range list {
		if c.Name == name {
			s, err := r.GetSecret(ctx, c.ID)
			must(err)
			return s
		}
	}
	log.Fatalf("  ✗ 凭证库里没有 %q", name)
	return ""
}

func must(err error) {
	if err != nil {
		log.Fatalf("  ✗ %v", err)
	}
}
