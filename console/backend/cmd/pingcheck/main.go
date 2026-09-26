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
	"io"
	"log"
	"os"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
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

	// The same verification the console's health probe runs on an agent
	// (pki.VerifyAgentChain): chain to this CA with ServerAuth usage and a valid
	// date, role agent, and the CN pinned to the remote named by -remote.
	tlsCfg, err := pki.AgentDialTLSConfig(pair, pool, *remote)
	must(err)
	reportVerifiedAgent(tlsCfg, os.Stdout)

	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: *addr,
		RelayName:      "pingcheck",
		RemoteName:     *remote,
		TLS:            tlsCfg,
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

// reportVerifiedAgent makes cfg print the agent's CN to w each time its
// existing VerifyConnection accepts the peer, and only then: a refused peer is
// never named on the operator's screen as if it were the agent. A config with
// no VerifyConnection to wrap refuses every peer instead of printing it.
func reportVerifiedAgent(cfg *tls.Config, w io.Writer) {
	verifyAgent := cfg.VerifyConnection
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if verifyAgent == nil {
			return errors.New("no agent verification configured; refusing the peer")
		}
		if err := verifyAgent(cs); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  agent 证书  : CN=%s (role=agent, 由本 CA 签发 ✓)\n", cs.PeerCertificates[0].Subject.CommonName)
		return nil
	}
}

// secretNamed reads the console's own row through the same fail-closed
// selection the console uses: a missing row, or rows under the name that the
// console did not write, stop the tool instead of handing it a look-alike.
func secretNamed(ctx context.Context, r *repository.CredentialRepository, name string) string {
	list, err := r.List(ctx)
	must(err)
	row, err := models.SelectConsoleRow(list, name)
	must(err)
	s, err := r.GetSecret(ctx, row.ID)
	must(err)
	return s
}

func must(err error) {
	if err != nil {
		log.Fatalf("  ✗ %v", err)
	}
}
