// Command qubes-air-mcp is the stdio MCP server for the Qubes Air console
// (docs/mcp-design.md). It runs as an independent process inside the console
// AppVM and reaches the Console API only as a loopback HTTP client; it holds no
// CA key, no provider credentials and no data-plane privilege of its own.
//
// The Console API bearer token is read ONLY from the QUBES_AIR_MCP_TOKEN
// environment variable. It is deliberately not a flag: a flag would expose it
// through `ps`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/slchris/qubes-air/console/internal/mcp"
)

func main() {
	os.Exit(run())
}

// run holds main's body and returns an exit code instead of calling os.Exit,
// so the deferred signal cleanup below actually runs (gocritic exitAfterDefer).
func run() int {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	// stdout carries the MCP protocol: every log line must go to stderr or it
	// would corrupt the newline-delimited JSON stream.
	log.SetOutput(os.Stderr)

	apiURL := flag.String("api-url", "http://127.0.0.1:8080", "Console API base URL (must reach only over loopback for production use)")
	scopeFlag := flag.String("scope", string(mcp.ScopeReadOnly), "tool scope: read-only or control")
	enableComputerUse := flag.Bool("enable-computer-use", false, "register the computer-use tool group (stubs; not implemented in this phase)")
	allowNonLoopback := flag.Bool("allow-non-loopback", false, "permit a non-loopback --api-url; it must then be https, or the bearer token travels in cleartext")
	flag.Parse()

	scope, ok := mcp.ParseScope(*scopeFlag)
	if !ok {
		fmt.Fprintf(os.Stderr, "invalid --scope %q: must be %q or %q\n",
			*scopeFlag, mcp.ScopeReadOnly, mcp.ScopeControl)
		return 2
	}

	token := os.Getenv("QUBES_AIR_MCP_TOKEN")
	if token == "" {
		log.Printf("warning: QUBES_AIR_MCP_TOKEN is not set; requests will carry no Authorization header")
	}
	if err := validateAPIURL(*apiURL, *allowNonLoopback); err != nil {
		fmt.Fprintf(os.Stderr, "refusing --api-url %s: %v\n", logSafe(*apiURL), err)
		return 2
	}

	client := mcp.NewClient(*apiURL, token)
	registry := mcp.NewRegistry(scope, *enableComputerUse, client)
	codec := mcp.NewCodec(os.Stdin, os.Stdout)
	server := mcp.NewServer(codec, registry)

	log.Printf("qubes-air-mcp serving on stdio: scope=%s tools=%d computer_use=%v api=%s", //nolint:gosec // G706: the api URL is passed through logSafe, which strips control characters before it reaches the log
		scope, len(registry.Tools()), *enableComputerUse, logSafe(*apiURL))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := server.Serve(ctx); err != nil {
		if errors.Is(err, mcp.ErrLineTooLong) {
			log.Printf("mcp: refusing an over-long frame; exiting")
			return 1
		}
		if ctx.Err() != nil {
			return 0
		}
		log.Printf("mcp: %v", err)
		return 1
	}
	return 0
}

// logSafe strips control characters so an operator-supplied value cannot forge
// or break a log line (gosec G706).
func logSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// validateAPIURL enforces the loopback-only default.
//
// A non-loopback Console API is refused unless the operator opts in with
// --allow-non-loopback, and even then the URL must be https: over plain http
// the bearer token would cross the network in cleartext. This is a refusal
// rather than a warning because the token is the whole authority of this
// process — a warning is something an operator scrolls past.
func validateAPIURL(raw string, allowNonLoopback bool) error {
	host := urlHost(raw)
	if loopbackHost(host) {
		return nil
	}
	if !allowNonLoopback {
		return fmt.Errorf("host %q is not loopback: this process is designed to reach the Console API over 127.0.0.1 (pass --allow-non-loopback to override)", host)
	}
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("host %q is not loopback and the URL is not https: the bearer token would travel in cleartext", host)
	}
	return nil
}

// urlHost extracts the host portion of a URL, tolerating a bare host:port.
func urlHost(raw string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "https://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && strings.IndexByte(s, ']') < i {
		s = s[:i]
	}
	return s
}

// loopbackHost reports whether host names the local machine.
func loopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost", "[::1]":
		return true
	}
	return strings.HasPrefix(host, "127.")
}
