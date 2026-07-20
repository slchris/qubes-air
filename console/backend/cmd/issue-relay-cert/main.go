// Command issue-relay-cert signs a relay's client-certificate request.
//
// It is the console-side half of relay certificate provisioning for the
// separate-relay transport (docs/grpc-transport-design.md §0.5). A relay qube
// generates its own keypair, sends only a CSR, and this command — invoked by the
// qubesair.IssueRelayCert qrexec service on the console qube — signs it with the
// console CA and returns the certificate. The relay's private key never leaves
// the relay, and the console never enters the data path: it is only the CA.
//
// Authentication is dom0's, not a token's. The caller's qube name arrives as an
// argument from QREXEC_REMOTE_DOMAIN, which dom0 sets and a source qube cannot
// forge, and the CSR's common name is pinned to it — so a qube can only ever
// obtain a certificate for its OWN relay identity, and only if dom0 policy let
// it reach this service at all. The CSR is read from stdin; the signed
// certificate (plus the CA certificate) is written to stdout as JSON.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// A qube name as dom0 reports it: a letter or digit, then the usual safe set.
// The caller name comes from QREXEC_REMOTE_DOMAIN and is already trustworthy,
// but a value that is not a plausible qube name has no business becoming a CN.
var qubeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// relayCertLifetime is short by design: the relay renews on a timer, so a
// leaked certificate ages out quickly, and the relay is always reachable to
// re-issue (it is a local qube, not a remote that might be offline for days).
const relayCertLifetime = 24 * time.Hour

func main() {
	log.SetFlags(0)
	log.SetPrefix("issue-relay-cert: ")

	dsn := flag.String("db", "", "console sqlite DSN")
	lifetime := flag.Duration("lifetime", relayCertLifetime, "certificate lifetime")
	flag.Parse()

	encKey := os.Getenv("QUBES_AIR_ENCRYPTION_KEY")
	if encKey == "" {
		log.Fatal("QUBES_AIR_ENCRYPTION_KEY is required")
	}
	args := flag.Args()
	if len(args) < 1 {
		log.Fatal("usage: issue-relay-cert [flags] <caller-qube-name>   (CSR on stdin)")
	}
	caller := args[0]

	csrPEM, err := io.ReadAll(os.Stdin)
	must(err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := database.New(&database.Config{DSN: *dsn})
	must(err)
	defer db.Close()

	kr, err := keyring.NewSingle([]byte(encKey))
	must(err)
	creds := repository.NewCredentialRepository(db, kr)

	ca, err := pki.ParseCA(secretNamed(ctx, creds, "qubes-air-ca-cert"),
		secretNamed(ctx, creds, "qubes-air-ca-key"))
	must(err)

	signed, err := issueRelayCert(ca, caller, string(csrPEM), *lifetime)
	if err != nil {
		log.Fatalf("refused: %v", err)
	}

	out, err := json.Marshal(signed)
	must(err)
	_, _ = os.Stdout.Write(out)
}

// issueRelayCert pins the CSR's common name to the caller's relay identity and
// signs it. Kept separate from main so the pin-and-sign logic is testable
// without a database: everything security-relevant happens here.
func issueRelayCert(ca *pki.CA, caller, csrPEM string, lifetime time.Duration) (*pki.SignedCert, error) {
	caller = strings.TrimSpace(caller)
	if caller == "" {
		return nil, errors.New("empty caller qube name")
	}
	if !qubeNameRe.MatchString(caller) {
		return nil, fmt.Errorf("caller %q is not a valid qube name", caller)
	}
	// SignAgentCSR refuses a CSR whose CN differs from this, so a relay cannot
	// request another relay's (or an agent's) identity even if policy let it in.
	return ca.SignAgentCSR(csrPEM, pki.RelayCommonName(caller), lifetime)
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
		log.Fatal(strings.TrimSpace(err.Error()))
	}
}
