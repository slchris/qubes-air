// Command list-endpoints prints each running qube's agent endpoint.
//
// It is the console side of endpoint delivery for the separate-relay transport
// (docs/grpc-transport-design.md §0.5): the relay calls the qubesair.RemoteEndpoints
// qrexec service, which runs this, and writes each line into its own QubesDB so
// the qubesair.GrpcProxy handler can resolve a RemoteVM's address without the
// console being in the per-call path. The relay refreshes on a short timer, so a
// newly provisioned qube becomes reachable within one interval.
//
// Output is one "<name> <ip>:<port>" per line. Only names and addresses cross
// this channel — never credentials — so unlike issue-relay-cert it needs no
// encryption key, only the database.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("list-endpoints: ")

	dsn := flag.String("db", "", "console sqlite DSN")
	port := flag.String("port", "8443", "agent mTLS port")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := database.New(&database.Config{DSN: *dsn})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	qubes, err := repository.NewQubeRepository(db).List(ctx, repository.DefaultQubeListOptions())
	if err != nil {
		log.Fatal(err)
	}

	var b strings.Builder
	for _, q := range qubes {
		// Only running qubes with an address are reachable; a released or
		// pending row has nothing to forward to and must not shadow a live one.
		if q.Status != models.QubeStatusRunning || q.IPAddress == "" {
			continue
		}
		fmt.Fprintf(&b, "%s %s:%s\n", q.Name, q.IPAddress, *port)
	}
	// One write, so a caller reading line by line never sees a half-line.
	fmt.Print(b.String())
}
