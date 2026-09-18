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
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	// stdout carries the MCP protocol: every log line must go to stderr or it
	// would corrupt the newline-delimited JSON stream.
	log.SetOutput(os.Stderr)

	apiURL := flag.String("api-url", "http://127.0.0.1:8080", "Console API base URL (must reach only over loopback for production use)")
	scopeFlag := flag.String("scope", string(mcp.ScopeReadOnly), "tool scope: read-only or control")
	enableComputerUse := flag.Bool("enable-computer-use", false, "register the computer-use tool group (stubs; not implemented in this phase)")
	flag.Parse()

	scope, ok := mcp.ParseScope(*scopeFlag)
	if !ok {
		fmt.Fprintf(os.Stderr, "invalid --scope %q: must be %q or %q\n",
			*scopeFlag, mcp.ScopeReadOnly, mcp.ScopeControl)
		os.Exit(2)
	}

	token := os.Getenv("QUBES_AIR_MCP_TOKEN")
	if token == "" {
		log.Printf("warning: QUBES_AIR_MCP_TOKEN is not set; requests will carry no Authorization header")
	}
	if host := urlHost(*apiURL); !loopbackHost(host) {
		log.Printf("warning: --api-url host %q is not loopback; the MCP process was designed to talk to the Console API over 127.0.0.1", host)
	}

	client := mcp.NewClient(*apiURL, token)
	registry := mcp.NewRegistry(scope, *enableComputerUse, client)
	codec := mcp.NewCodec(os.Stdin, os.Stdout)
	server := mcp.NewServer(codec, registry)

	log.Printf("qubes-air-mcp serving on stdio: scope=%s tools=%d computer_use=%v api=%s",
		scope, len(registry.Tools()), *enableComputerUse, *apiURL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := server.Serve(ctx); err != nil {
		if errors.Is(err, mcp.ErrLineTooLong) {
			log.Printf("mcp: refusing an over-long frame; exiting")
			os.Exit(1)
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("mcp: %v", err)
		os.Exit(1)
	}
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
