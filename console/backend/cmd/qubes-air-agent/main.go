// Command qubes-air-agent is the RemoteVM agent.
//
// It runs on a non-Qubes remote (a cloud image cloned by terraform) and gives
// that host qrexec semantics without Xen vchan — see docs/remote-agent-design.md
// for why qrexec-client-vm cannot simply be installed there.
//
// It replaces the sleep-infinity placeholder that packer/scripts/install-agent.sh
// used to ship. That script is gone: the binary is no longer baked into the VM
// image but packaged by packaging/agent-deb/ and installed at first boot, which
// is also where the systemd unit now lives.
//
// Connection direction: this process LISTENS and the local relay dials in,
// matching the existing transport (internal/transport/grpc: client.go is the
// local relay, server.go the remote). Note that this does NOT give the remote
// zero inbound — a claim terraform/providers/proxmox/zero-inbound-firewall.md
// makes while also stating the local side dials out, which cannot both be true.
// Reversing it is a transport change, not an agent change.
//
// Security posture: this host is UNTRUSTED. The service allowlist here guards
// against misconfiguration, not against an attacker who has the box — whoever
// controls it can replace this binary. Authorization lives in the local dom0
// policy, which decided before the call ever left the trusted side.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/slchris/qubes-air/console/internal/agent"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

// buildVersion is stamped at link time:
//
//	go build -ldflags "-X main.buildVersion=$(git describe --tags --always)"
//
// It is reported in the handshake for observability and never participates in a
// compatibility decision — see internal/transport/grpc/frames.go.
var buildVersion = "dev"

// defaultAllowedServices is the starting allowlist.
//
// qubesair.Ping is the reachability probe QubeService.CheckReachable calls; it
// answers "pong <remote_name> <unix_ts>". Deliberately minimal: a probe should
// answer "is this link up", not "is this host healthy", or one failure becomes
// several indistinguishable ones.
var defaultAllowedServices = []string{
	"qubesair.Ping",
	"qubesair.Status",
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	var (
		listen      = flag.String("listen", "0.0.0.0:8443", "host:port to listen on")
		remoteName  = flag.String("remote-name", "", "this remote's name (aligns with RemoteVM remote_name)")
		serviceDir  = flag.String("service-dir", agent.DefaultServiceDir, "directory holding qrexec service implementations")
		allowedCSV  = flag.String("allow", strings.Join(defaultAllowedServices, ","), "comma-separated services this agent may run")
		caFile      = flag.String("ca", "", "PEM CA bundle used to verify the relay's client certificate (required)")
		certFile    = flag.String("cert", "", "PEM server certificate (required)")
		keyFile     = flag.String("key", "", "PEM server private key (required)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("qubes-air-agent %s\n", buildVersion)
		return
	}

	if *remoteName == "" {
		// Fall back to the hostname rather than refusing: a remote that has not
		// been told its name can still answer a probe, and reporting the wrong
		// name is easier to diagnose than a process that will not start.
		h, err := os.Hostname()
		if err != nil {
			log.Fatalf("no --remote-name and hostname unavailable: %v", err)
		}
		*remoteName = h
		log.Printf("no --remote-name given, using hostname %q", h)
	}

	tlsCfg, err := buildTLS(*caFile, *certFile, *keyFile)
	if err != nil {
		log.Fatalf("TLS: %v", err)
	}

	inv := agent.NewLocalInvoker(*remoteName, splitCSV(*allowedCSV))
	inv.ServiceDir = *serviceDir

	log.Printf("qubes-air-agent %s starting", buildVersion)
	log.Printf("  remote name : %s", *remoteName)
	log.Printf("  listen      : %s", *listen)
	log.Printf("  service dir : %s", inv.ServiceDir)
	log.Printf("  allowed     : %s", *allowedCSV)
	warnMissingServices(inv.ServiceDir, splitCSV(*allowedCSV))

	srv := transportgrpc.NewServer(transportgrpc.ServerConfig{
		Listen: *listen,
		TLS:    tlsCfg,
		// No CertRegistry here: the registry lives with the issuer, on the
		// trusted side. This agent verifies that the peer's certificate chains
		// to the CA; deciding whether a given relay is still permitted is not
		// this host's call to make, and a revocation list stored on an
		// untrusted machine could simply be deleted.
	}, inv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx); err != nil {
		log.Fatalf("serve: %v", err)
	}
	log.Printf("qubes-air-agent stopped")
}

// buildTLS loads the server certificate and the CA used to verify the relay.
//
// All three are required. Running without mTLS would mean anyone who can reach
// the port may execute this host's qrexec services, and on a LAN that is
// everyone — so a missing file is a startup failure, not a warning.
func buildTLS(caFile, certFile, keyFile string) (*tls.Config, error) {
	if caFile == "" || certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("--ca, --cert and --key are all required (mTLS is mandatory)")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load key pair: %w", err)
	}
	caPEM, err := os.ReadFile(caFile) // #nosec G304 -- operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA file %q contains no usable certificate", caFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// warnMissingServices reports allowed services with no implementation.
//
// Worth saying at startup: an allowlisted service whose script was never
// installed fails only when someone calls it, and then looks like a transport
// fault rather than a missing file.
func warnMissingServices(dir string, allowed []string) {
	for _, s := range allowed {
		path := dir + "/" + s
		if _, err := os.Stat(path); err != nil {
			log.Printf("  WARNING service %q is allowed but %s does not exist; calls to it will fail", s, path)
		}
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
