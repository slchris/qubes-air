// Command list-endpoints prints each running qube's agent endpoint.
//
// It is the console side of endpoint delivery for the separate-relay transport
// (docs/grpc-transport-design.md §0.5): the relay calls the qubesair.RemoteEndpoints
// qrexec service, which runs this, and writes each line into its own QubesDB so
// the qubesair.GrpcProxy handler can resolve a RemoteVM's address without the
// console being in the per-call path. The relay refreshes on a short timer, so a
// newly provisioned qube becomes reachable within one interval.
//
// Output is a complete, versioned snapshot with a count and SHA-256 trailer.
// Only names and addresses cross this channel, never credentials. The receiver
// must validate the whole snapshot before using its absence to prune a cache.
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/buildinfo"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("list-endpoints: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run executes the command. Errors are returned so main's log.Fatal runs only
// after the deferred context cancel and database close have completed.
func run() error {
	showVersion := flag.Bool("version", false, "show build identity and exit")
	dsn := flag.String("db", "./qubes-air.db", "existing console sqlite file")
	port := flag.String("port", "8443", "agent mTLS port")
	flag.Parse()
	if *showVersion {
		fmt.Printf("list-endpoints %s\n", buildinfo.Get())
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := database.OpenReadOnly(ctx, *dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	snapshot, err := endpointSnapshot(ctx, db, *port)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(os.Stdout, snapshot)
	return err
}

const maxEndpoints = 10000

var endpointNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,23}$`)

func endpointSnapshot(ctx context.Context, db *database.DB, port string) (string, error) {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", fmt.Errorf("invalid agent port")
	}
	// One SQL statement is one SQLite read snapshot. Never page across changing
	// rows or truncate silently: absence authorizes Relay cache removal.
	qubes, err := repository.NewQubeRepository(db).List(ctx, repository.QubeListOptions{
		Status: string(models.QubeStatusRunning), Limit: maxEndpoints + 1,
	})
	if err != nil {
		return "", err
	}
	if len(qubes) > maxEndpoints {
		return "", fmt.Errorf("endpoint snapshot exceeds %d running qubes", maxEndpoints)
	}

	var b strings.Builder
	count := 0
	seen := make(map[string]bool)
	for _, q := range qubes {
		// Only running qubes with an address are reachable; a released or
		// pending row has nothing to forward to and must not shadow a live one.
		if q.Status != models.QubeStatusRunning || q.IPAddress == "" {
			continue
		}
		ip, err := netip.ParseAddr(q.IPAddress)
		if !endpointNameRE.MatchString(q.Name) || seen[q.Name] || err != nil || !ip.Is4() {
			return "", fmt.Errorf("invalid or duplicate running endpoint record")
		}
		seen[q.Name] = true
		count++
		fmt.Fprintf(&b, "%s %s:%s\n", q.Name, ip.String(), port)
	}
	records := b.String()
	return fmt.Sprintf("QUBES-AIR-ENDPOINTS 1\n%sEND %d %x\n", records, count, sha256.Sum256([]byte(records))), nil
}
