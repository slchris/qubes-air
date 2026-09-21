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
	"github.com/slchris/qubes-air/console/internal/transport"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("relay-call: ")
	if err := run(); err != nil {
		// Propagate a remote command's exit code instead of collapsing every
		// failure to 1, so a caller (or dom0 policy) sees the real status.
		var ec exitCodeError
		if errors.As(err, &ec) {
			os.Exit(ec.code)
		}
		log.Fatal(logSafe(strings.TrimSpace(err.Error())))
	}
}

// exitCodeError carries a remote service's exit code up to main.
type exitCodeError struct {
	code int
	msg  string
}

func (e exitCodeError) Error() string { return e.msg }

// run executes one relay call. Failures are returned rather than logged so
// that main's log.Fatal runs only after every deferred cleanup (the context
// cancel and, in mint mode, the database close) has completed.
func run() error {
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
	// Stream mode: a raw bidirectional TCP proxy to a loopback port on the remote
	// (qubesair.ConnectTCP uses this for GUI). The positional service becomes the
	// PORT, and stdin/stdout are piped live rather than buffered.
	stream := flag.Bool("stream", false, "raw TCP stream to a remote loopback port (service arg is the port)")
	flag.Parse()

	streamMode := *stream
	target, service, err := parseRelayTarget(streamMode, flag.Args())
	if err != nil {
		return err
	}

	// Buffered calls take their request body from stdin; a stream pipes stdin live
	// (do NOT drain it here) inside dialAndStream.
	var body []byte
	if !streamMode {
		body, err = io.ReadAll(os.Stdin)
		must(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	pair, pool, endpoint, remoteName, err := resolveRelayCredentials(ctx, relayCredentialFlags{
		dsn:  *dsn,
		port: *port,
		addr: *addr,
		cert: *certFile,
		key:  *keyFile,
		ca:   *caFile,
	}, target)
	if err != nil {
		return err
	}

	log.Printf("target=%s service=%s endpoint=%s stream=%v", remoteName, service, endpoint, streamMode)
	if streamMode {
		// Pipe stdin ↔ remote loopback port ↔ stdout over mTLS; no LAN port.
		if err := dialAndStream(ctx, pair, pool, endpoint, remoteName, service); err != nil {
			return fmt.Errorf("stream failed: %w", err)
		}
		return nil
	}
	res, err := dialAndCall(ctx, pair, pool, endpoint, remoteName, service, body)
	if err != nil {
		return fmt.Errorf("call failed: %w", err)
	}
	// stdout and stderr are separate streams now; write each where it belongs
	// rather than merging them, so a caller can redirect them independently.
	if len(res.Stdout) > 0 {
		_, _ = os.Stdout.Write(res.Stdout)
	}
	if len(res.Stderr) > 0 {
		_, _ = os.Stderr.Write(res.Stderr)
	}
	if res.ExitCode != 0 {
		return exitCodeError{code: res.ExitCode,
			msg: fmt.Sprintf("remote service %q exited %d", service, res.ExitCode)}
	}
	return nil
}

// parseRelayTarget derives the target device and the remote service from the
// positional arguments. In stream mode the positional service argument is the
// remote loopback port, rewritten into the StreamTCP service form the agent
// dials; the agent side then dials 127.0.0.1:<port>.
func parseRelayTarget(stream bool, args []string) (target, service string, err error) {
	if len(args) < 2 {
		return "", "", errors.New("usage: relay-call [flags] <target> <service>   (or -stream <target> <port>)")
	}
	target, service = args[0], args[1]
	if stream {
		service = "qubesair.StreamTCP+" + args[1]
	}
	return target, service, nil
}

// relayCredentialFlags carries the plain flags that decide which credential
// path relay-call takes: provisioned (cert/key/CA on disk, address required)
// or mint (console CA from the database, address resolved by name when it is
// omitted).
type relayCredentialFlags struct {
	dsn, port, addr, cert, key, ca string
}

// resolveRelayCredentials builds the mTLS client material for one call.
//
// Provisioned mode reads a console-issued client certificate and the CA from
// disk and refuses to run without an explicit address: a relay that does not
// hold the CA cannot resolve endpoints from the console database either — the
// address is passed in (the relay's transport handler reads it from QubesDB).
// Mint mode (console-as-relay) reads the CA from the console database and signs
// a short-lived client certificate on the spot, resolving the endpoint from the
// database when -addr was not given. The returned remoteName is the name the
// agent is expected to answer to (it may differ from target in mint mode).
func resolveRelayCredentials(ctx context.Context, f relayCredentialFlags, target string) (pair tls.Certificate, pool *x509.CertPool, endpoint, remoteName string, err error) {
	endpoint = f.addr
	if f.cert != "" || f.key != "" || f.ca != "" {
		if f.cert == "" || f.key == "" || f.ca == "" {
			return tls.Certificate{}, nil, "", "", errors.New("provisioned mode needs -cert, -key and -ca together")
		}
		if endpoint == "" {
			return tls.Certificate{}, nil, "", "", errors.New("provisioned mode needs -addr")
		}
		pair, pool = loadProvisioned(f.cert, f.key, f.ca)
		return pair, pool, endpoint, target, nil
	}

	encKey := os.Getenv("QUBES_AIR_ENCRYPTION_KEY")
	if encKey == "" {
		return tls.Certificate{}, nil, "", "", errors.New("QUBES_AIR_ENCRYPTION_KEY is required in mint mode")
	}
	db, err := database.New(&database.Config{DSN: f.dsn})
	must(err)
	defer db.Close()
	kr, err := keyring.NewSingle([]byte(encKey))
	must(err)
	creds := repository.NewCredentialRepository(db, kr)
	if endpoint == "" {
		endpoint, target = resolveAgent(ctx, repository.NewQubeRepository(db), target, f.port)
	}
	ca, err := pki.ParseCA(secretNamed(ctx, creds, "qubes-air-ca-cert"),
		secretNamed(ctx, creds, "qubes-air-ca-key"))
	must(err)
	pair, pool = mintFromCA(ca)
	return pair, pool, endpoint, target, nil
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
func dialAndCall(ctx context.Context, pair tls.Certificate, pool *x509.CertPool, endpoint, remoteName, service string, in []byte) (transport.Result, error) {
	cli := newClient(pair, pool, endpoint, remoteName)
	go func() { _ = cli.Start(ctx) }()

	// The tunnel comes up asynchronously, so retry ONLY while it is not yet
	// connected. Once a call has been dispatched, any error is returned as-is:
	// retrying could re-run a command with side effects (apt-get install, a
	// script that appends to a file), and a slow command must be waited on, not
	// retried. This also stops a mid-call deadline from being masked by a
	// trailing "tunnel not connected".
	var res transport.Result
	var err error
	for {
		res, err = cli.CallResult(ctx, remoteName, service, in)
		if err == nil {
			return res, nil
		}
		if !errors.Is(err, transportgrpc.ErrNotConnected) {
			return transport.Result{}, err
		}
		select {
		case <-ctx.Done():
			return transport.Result{}, fmt.Errorf("tunnel never connected within deadline: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// newClient builds the transport client with the console health-probe TLS setup:
// the agent's certificate has no SAN for a bare IP, so the chain is verified by
// hand in VerifyConnection.
func newClient(pair tls.Certificate, pool *x509.CertPool, endpoint, remoteName string) *transportgrpc.Client {
	return transportgrpc.NewClient(transportgrpc.ClientConfig{
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
				leaf := cs.PeerCertificates[0]
				inters := x509.NewCertPool()
				for _, c := range cs.PeerCertificates[1:] {
					inters.AddCert(c)
				}
				if _, err := leaf.Verify(x509.VerifyOptions{
					Roots:         pool,
					Intermediates: inters,
					KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				}); err != nil {
					return err
				}
				// Role and name, not just "chained to our CA": every qube holds
				// a CA-signed certificate, so without these any one of them
				// authenticates as any other on the shared L2 bridge.
				if role, err := pki.RoleOf(leaf); err != nil {
					return err
				} else if role != pki.RoleAgent {
					return fmt.Errorf("peer role %q is not an agent", role)
				}
				if want := pki.AgentCommonName(remoteName); leaf.Subject.CommonName != want {
					return fmt.Errorf("agent certificate identifies %q but this endpoint should be serving %q",
						leaf.Subject.CommonName, want)
				}
				return nil
			},
		},
	}, nil)
}

// dialAndStream proxies a raw bidirectional stream: os.Stdin → the remote's
// loopback port → os.Stdout, over the agent's mTLS Tunnel (service
// qubesair.StreamTCP+<port>). This is how GUI rides mTLS with no port exposed on
// the remote's LAN. Waits for the tunnel, then streams until either side closes.
func dialAndStream(ctx context.Context, pair tls.Certificate, pool *x509.CertPool, endpoint, remoteName, service string) error {
	cli := newClient(pair, pool, endpoint, remoteName)
	go func() { _ = cli.Start(ctx) }()
	for {
		// CallStream returns ErrNotConnected without touching stdin until the
		// tunnel is up, so retrying it is safe.
		err := cli.CallStream(ctx, remoteName, service, os.Stdin, os.Stdout)
		if !errors.Is(err, transportgrpc.ErrNotConnected) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("tunnel never connected within deadline: %w", ctx.Err())
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
		log.Fatal(logSafe(strings.TrimSpace(err.Error()))) //nolint:gosec // G706: logSafe strips control characters before the value reaches the log
	}
}

// logSafe strips control characters so an operator-supplied or remote value
// cannot forge or break a log line (gosec G706).
func logSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
