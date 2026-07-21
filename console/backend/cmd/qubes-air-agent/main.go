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
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
// qubesair.Status is deliberately NOT here. It was allowed before anything
// implemented it, so every agent logged a warning on every start that the
// service is allowed but missing. A warning that is always present is one
// operators learn to scroll past, which costs more than the missing service.
// Add it back in the same change that ships /etc/qubes-rpc/qubesair.Status.
var defaultAllowedServices = []string{
	"qubesair.Ping",
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	var (
		listen     = flag.String("listen", "0.0.0.0:8443", "host:port to listen on")
		remoteName = flag.String("remote-name", "", "this remote's name (aligns with RemoteVM remote_name)")
		serviceDir = flag.String("service-dir", agent.DefaultServiceDir, "directory holding qrexec service implementations")
		allowedCSV = flag.String("allow", strings.Join(defaultAllowedServices, ","), "comma-separated services this agent may run")
		caFile     = flag.String("ca", "", "PEM CA bundle used to verify the relay's client certificate (required)")
		certFile   = flag.String("cert", "", "PEM server certificate (required)")
		keyFile    = flag.String("key", "", "PEM server private key (required)")
		tokenFile  = flag.String("bootstrap-token", "/etc/qubes-air/bootstrap-token",
			"path to the one-shot bootstrap token; consulted only when no identity is installed yet")
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

	if *caFile == "" || *certFile == "" || *keyFile == "" {
		// Running without mTLS would mean anyone who can reach the port may
		// execute this host's qrexec services, and on a LAN that is everyone —
		// so a missing file is a startup failure, not a warning. The cert and
		// key may not EXIST yet — that is what bootstrap is for — but their
		// paths must be known, because bootstrap's job is to fill them.
		log.Fatalf("--ca, --cert and --key are all required (mTLS is mandatory)")
	}
	// Pending rather than loaded: a first boot has the CA (cloud-init delivers
	// it) but no certificate yet. Anything other than a cleanly absent pair —
	// corrupt files, unreadable key, half a pair — still fails startup here.
	identity, err := agent.NewPendingIdentity(*certFile, *keyFile, *caFile)
	if err != nil {
		log.Fatalf("TLS identity: %v", err)
	}

	// An empty allowlist is treated as allow-all by the invoker (len==0 skips the
	// check), so refuse to start with one rather than silently exposing every
	// service in ServiceDir. A locked-down agent sets --allow explicitly.
	allowed := splitCSV(*allowedCSV)
	if len(allowed) == 0 {
		log.Fatal("--allow is empty: refusing to start (an empty allowlist would allow every service)")
	}

	inv := agent.NewLocalInvoker(*remoteName, allowed)
	inv.ServiceDir = *serviceDir

	bootstrap := armBootstrapIfPending(identity, inv, *remoteName, *certFile, *tokenFile)

	// Certificate renewal. Without it the only way to replace an expiring
	// certificate is to rebuild the VM, because cloud-init delivers user-data
	// once at first boot — which turns the 90-day certificate lifetime into a
	// fleet rebuild deadline. Registered as builtins because they rewrite this
	// process's own key and swap the listener's certificate; a script in
	// ServiceDir could do neither, and must not be able to pretend it did.
	renewal := agent.NewRenewalService(identity, agent.DefaultPendingRenewalTTL)
	if err := renewal.RegisterBuiltins(inv); err != nil {
		log.Fatalf("register renewal services: %v", err)
	}

	log.Printf("qubes-air-agent %s starting", buildVersion)
	log.Printf("  remote name : %s", *remoteName)
	log.Printf("  listen      : %s", *listen)
	log.Printf("  service dir : %s", inv.ServiceDir)
	log.Printf("  allowed     : %s", *allowedCSV)
	if leaf, err := identity.Leaf(); err == nil {
		// Say which identity is actually loaded and when it runs out. A fleet
		// that stopped renewing has to be visible long before the certificates
		// expire; this is the cheapest place to see it on one host.
		log.Printf("  identity    : %s (expires %s)",
			leaf.Subject.CommonName, leaf.NotAfter.UTC().Format(time.RFC3339))
	} else {
		log.Printf("  identity    : none installed; bootstrap pending")
	}
	warnMissingServices(inv, inv.ServiceDir, splitCSV(*allowedCSV))

	// In bootstrap mode the certificate source falls back to the placeholder
	// until Install succeeds; afterwards, and in every ordinary start, it is
	// the identity itself.
	var certSource transportgrpc.ServerCertSource = identity
	if bootstrap != nil {
		certSource = bootstrap
	}

	srv := transportgrpc.NewServer(transportgrpc.ServerConfig{
		Listen: *listen,
		TLS:    identity.ServerTLSConfig(),
		// Certificate selection per handshake, so a renewal takes effect on the
		// next connection instead of the next restart — and without dropping
		// the tunnels that are already up.
		CertSource: certSource,
		// No CertRegistry here: the registry lives with the issuer, on the
		// trusted side. This agent verifies that the peer's certificate chains
		// to the CA; deciding whether a given relay is still permitted is not
		// this host's call to make, and a revocation list stored on an
		// untrusted machine could simply be deleted.
	}, inv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx); err != nil {
		// gocritic flags the skipped `defer stop()`. Accepted: stop() only
		// detaches the signal handler, and the process is terminating anyway.
		//nolint:gocritic // exitAfterDefer: the deferred work is moot at exit
		log.Fatalf("serve: %v", err)
	}
	log.Printf("qubes-air-agent stopped")
}

// armBootstrapIfPending puts the process into BOOTSTRAP mode when no identity
// is installed, and returns nil when one is.
//
// In bootstrap mode the listener presents a self-signed placeholder, still
// demands a client certificate chaining to the cloud-init CA, and serves two
// extra builtins through which the console — the only party that CA vouches
// for — trades the one-shot token for this host's first certificate. No
// restart follows: Install hands the listener the real certificate the same
// way a renewal does, on the next handshake.
//
// Fatal on any misconfiguration, like the rest of startup: a host with no
// identity AND no token has no way to ever become reachable, and dying loudly
// beats listening forever as a port nobody can authenticate to.
func armBootstrapIfPending(identity *agent.Identity, inv *agent.LocalInvoker, remoteName, certFile, tokenFile string) *agent.BootstrapService {
	if identity.HasCertificate() {
		return nil
	}

	tokenBytes, err := os.ReadFile(tokenFile) // #nosec G304 -- operator-supplied path
	if err != nil {
		log.Fatalf("no identity at %q and no bootstrap token at %q: %v — "+
			"this host was provisioned with neither credentials nor a way to obtain them",
			certFile, tokenFile, err)
	}
	bootstrap, err := agent.NewBootstrapService(
		identity, remoteName, strings.TrimSpace(string(tokenBytes)),
		func() {
			// The token authorized exactly one issuance and the console has
			// redeemed it; the file is now inert, but scrubbing it keeps a
			// later image or backup of this disk from carrying a credential
			// that LOOKS live.
			if rmErr := os.Remove(tokenFile); rmErr != nil {
				log.Printf("bootstrap: could not remove spent token file %q: %v", tokenFile, rmErr)
			}
		})
	if err != nil {
		log.Fatalf("bootstrap mode: %v", err)
	}
	if err := bootstrap.RegisterBuiltins(inv); err != nil {
		log.Fatalf("register bootstrap services: %v", err)
	}
	log.Printf("no identity installed; awaiting bootstrap (token from %s)", tokenFile)
	return bootstrap
}

// warnMissingServices reports allowed services with no implementation.
//
// Worth saying at startup: an allowlisted service whose script was never
// installed fails only when someone calls it, and then looks like a transport
// fault rather than a missing file.
//
// Builtins are skipped. They have no file in ServiceDir and never will — see
// internal/agent/builtin.go — so warning about one would send an operator
// looking for a script that must not exist.
func warnMissingServices(inv *agent.LocalInvoker, dir string, allowed []string) {
	for _, s := range allowed {
		if inv.IsBuiltin(s) {
			continue
		}
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
