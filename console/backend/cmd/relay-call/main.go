// Command relay-call is the guts of the RemoteVM gRPC transport, run on the
// relay (the console qube) once per qrexec call.
//
// When a local qube runs `qrexec-client-vm <remote> qubesair.Ping`, dom0
// rewrites it to a single qrexec call against the RemoteVM's relayvm, invoking
// the transport_rpc service `qubesair.GrpcProxy+<target>+<service>`. The
// /etc/qubes-rpc/qubesair.GrpcProxy wrapper parses that argument and execs this
// binary as `relay-call <target> <service>`, piping the caller's stdin through
// and handing the agent's reply straight back on stdout.
//
// It does exactly what the console's health probe already does — mint a client
// certificate from the console CA, dial the agent's mTLS gRPC port, and Call —
// only for an arbitrary service rather than the hard-coded Ping, and it resolves
// the target's address from the console database instead of a flag. That reuse
// is deliberate: the health path is proven on hardware, so the transport a local
// qube reaches is the same one the console already trusts.
//
// stdout carries ONLY the agent's response bytes, so qrexec can forward it
// verbatim; every diagnostic goes to stderr.
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
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("relay-call: ")

	dsn := flag.String("db", "", "console sqlite DSN (mint mode)")
	port := flag.String("port", "8443", "agent mTLS port")
	// The address may be given explicitly; in provisioned mode it is required,
	// in mint mode it defaults to a database lookup by target name.
	addr := flag.String("addr", "", "agent host:port (required in provisioned mode)")
	// Provisioned mode: a relay that holds a console-issued client certificate on
	// disk (see cmd/relay-bootstrap) rather than the CA that mints one. Giving
	// all three switches the tool off the database entirely.
	certFile := flag.String("cert", "", "client certificate PEM (provisioned mode)")
	keyFile := flag.String("key", "", "client key PEM (provisioned mode)")
	caFile := flag.String("ca", "", "CA certificate PEM (provisioned mode)")
	// Generous by default: this carries qubesair.Exec, and a command like
	// `apt-get install` easily outruns a short deadline. The deadline is
	// propagated to the agent, which caps a single call at its own timeout.
	timeout := flag.Duration("timeout", 180*time.Second, "overall deadline")
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		log.Fatal("usage: relay-call [flags] <target> <service>")
	}
	target := args[0]
	service := args[1]

	// The caller's request body arrives on stdin (empty for Ping). Read it all
	// before dialing so a slow reader cannot hold the tunnel open.
	body, err := io.ReadAll(os.Stdin)
	must(err)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var (
		pair     tls.Certificate
		pool     *x509.CertPool
		endpoint = *addr
	)
	if *certFile != "" || *keyFile != "" || *caFile != "" {
		// Provisioned mode: the relay does not hold the CA, so it cannot resolve
		// endpoints from the console database either — the address is passed in
		// (the relay's transport handler reads it from QubesDB).
		if *certFile == "" || *keyFile == "" || *caFile == "" {
			log.Fatal("provisioned mode needs -cert, -key and -ca together")
		}
		if endpoint == "" {
			log.Fatal("provisioned mode needs -addr")
		}
		pair, pool = loadProvisioned(*certFile, *keyFile, *caFile)
	} else {
		// Mint mode (console-as-relay): read the CA from the console database and
		// sign a short-lived client certificate on the spot, resolving the
		// endpoint from the database when -addr was not given.
		encKey := os.Getenv("QUBES_AIR_ENCRYPTION_KEY")
		if encKey == "" {
			log.Fatal("QUBES_AIR_ENCRYPTION_KEY is required in mint mode")
		}
		db, err := database.New(&database.Config{DSN: *dsn})
		must(err)
		defer db.Close()
		kr, err := keyring.NewSingle([]byte(encKey))
		must(err)
		creds := repository.NewCredentialRepository(db, kr)
		if endpoint == "" {
			endpoint, target = resolveAgent(ctx, repository.NewQubeRepository(db), target, *port)
		}
		ca, err := pki.ParseCA(secretNamed(ctx, creds, "qubes-air-ca-cert"),
			secretNamed(ctx, creds, "qubes-air-ca-key"))
		must(err)
		pair, pool = mintFromCA(ca)
	}

	log.Printf("target=%s service=%s endpoint=%s", target, service, endpoint)
	out, err := dialAndCall(ctx, pair, pool, endpoint, target, service, body)
	if err != nil {
		log.Fatalf("call failed: %v", err)
	}
	// Response bytes only — this is what qrexec hands back to the local caller.
	_, _ = os.Stdout.Write(out)
}

// mintFromCA signs a fresh short-lived client certificate from the console CA —
// the console-as-relay path, where the relay is the qube that holds the CA.
func mintFromCA(ca *pki.CA) (tls.Certificate, *x509.CertPool) {
	bundle, err := ca.IssueAgentCert("console-relay", time.Hour)
	must(err)
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	must(err)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(bundle.CAPEM)) {
		log.Fatal("CA PEM did not parse")
	}
	return pair, pool
}

// loadProvisioned reads a console-issued client certificate and the CA from
// disk — the separate-relay path, where the relay holds only its own identity.
func loadProvisioned(certFile, keyFile, caFile string) (tls.Certificate, *x509.CertPool) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	must(err)
	caPEM, err := os.ReadFile(caFile)
	must(err)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		log.Fatalf("CA file %s did not parse", caFile)
	}
	return pair, pool
}

// resolveAgent finds the running target qube by name and returns its
// "<ip>:<port>" endpoint plus the name the agent is expected to answer to.
func resolveAgent(ctx context.Context, repo repository.QubeRepository, target, port string) (string, string) {
	qubes, err := repo.List(ctx, repository.DefaultQubeListOptions())
	must(err)
	var match *models.Qube
	for _, q := range qubes {
		if q.Name != target {
			continue
		}
		// Prefer a running qube that actually has an address; a released or
		// errored row of the same name must not shadow it.
		if q.Status == models.QubeStatusRunning && q.IPAddress != "" {
			match = q
			break
		}
		if match == nil {
			match = q
		}
	}
	if match == nil {
		log.Fatalf("no qube named %q in the console database", target)
	}
	if match.IPAddress == "" {
		log.Fatalf("qube %q has no IP address (status %s)", target, match.Status)
	}
	return match.IPAddress + ":" + port, match.Name
}

// dialAndCall dials the agent over mTLS with the given client certificate and
// invokes one service. It mirrors the console health probe's TLS setup: the
// agent's certificate carries no SAN for a bare IP, so the chain is verified by
// hand in VerifyConnection rather than by the stack. The certificate may have
// been minted from the CA (console-as-relay) or loaded from disk (separate
// relay) — dialing does not care which.
func dialAndCall(ctx context.Context, pair tls.Certificate, pool *x509.CertPool, endpoint, remoteName, service string, in []byte) ([]byte, error) {
	var err error
	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: endpoint,
		RelayName:      "console-relay",
		RemoteName:     remoteName,
		TLS: &tls.Config{
			Certificates:       []tls.Certificate{pair},
			RootCAs:            pool,
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, //nolint:gosec // chain checked in VerifyConnection
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("agent presented no certificate")
				}
				_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
					Roots:     pool,
					KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
				})
				return err
			},
		},
	}, nil)

	go func() { _ = cli.Start(ctx) }()

	// The tunnel comes up asynchronously, so retry ONLY while it is not yet
	// connected. Once a call has been dispatched, any error is returned as-is:
	// retrying could re-run a command with side effects (apt-get install, a
	// script that appends to a file), and a slow command must be waited on, not
	// retried. This also stops a mid-call deadline from being masked by a
	// trailing "tunnel not connected".
	var out []byte
	for {
		out, err = cli.Call(ctx, remoteName, service, in)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, transportgrpc.ErrNotConnected) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("tunnel never connected within deadline: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
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
	log.Fatalf("credential %q not found", name)
	return ""
}

func must(err error) {
	if err != nil {
		// Trim the noisy wrapping some errors carry so the stderr line stays
		// readable in a qrexec log.
		log.Fatal(strings.TrimSpace(err.Error()))
	}
}
