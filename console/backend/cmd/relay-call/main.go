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

	dsn := flag.String("db", "", "console sqlite DSN")
	port := flag.String("port", "8443", "agent mTLS port")
	// The address may be given explicitly for a one-shot test; normally it is
	// resolved from the database by target name.
	addr := flag.String("addr", "", "agent host:port (overrides db lookup)")
	timeout := flag.Duration("timeout", 45*time.Second, "overall deadline")
	flag.Parse()

	// Read from the environment, never a flag: command-line arguments are
	// world-readable through /proc on the same host.
	encKey := os.Getenv("QUBES_AIR_ENCRYPTION_KEY")
	if encKey == "" {
		log.Fatal("QUBES_AIR_ENCRYPTION_KEY is required")
	}

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

	db, err := database.New(&database.Config{DSN: *dsn})
	must(err)
	defer db.Close()

	kr, err := keyring.NewSingle([]byte(encKey))
	must(err)
	creds := repository.NewCredentialRepository(db, kr)

	endpoint := *addr
	remoteName := target
	if endpoint == "" {
		ep, rn := resolveAgent(ctx, repository.NewQubeRepository(db), target, *port)
		endpoint, remoteName = ep, rn
	}
	log.Printf("target=%s service=%s endpoint=%s", target, service, endpoint)

	ca, err := pki.ParseCA(secretNamed(ctx, creds, "qubes-air-ca-cert"),
		secretNamed(ctx, creds, "qubes-air-ca-key"))
	must(err)

	out, err := call(ctx, ca, endpoint, remoteName, service, body)
	if err != nil {
		log.Fatalf("call failed: %v", err)
	}
	// Response bytes only — this is what qrexec hands back to the local caller.
	_, _ = os.Stdout.Write(out)
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

// call dials the agent over mTLS with a freshly minted client certificate and
// invokes one service. It mirrors the console health probe's TLS setup: the
// agent's certificate carries no SAN for a bare IP, so the chain is verified by
// hand in VerifyConnection rather than by the stack.
func call(ctx context.Context, ca *pki.CA, endpoint, remoteName, service string, in []byte) ([]byte, error) {
	bundle, err := ca.IssueAgentCert("console-relay", time.Hour)
	if err != nil {
		return nil, fmt.Errorf("mint client cert: %w", err)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(bundle.CAPEM)) {
		return nil, errors.New("CA PEM did not parse")
	}

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

	// The tunnel comes up asynchronously; retry Call until it does or the
	// deadline passes.
	var out []byte
	for {
		out, err = cli.Call(ctx, remoteName, service, in)
		if err == nil {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (last: %v)", ctx.Err(), err)
		case <-time.After(500 * time.Millisecond):
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
