// qubes-air-desktop is the fixed local client and non-root desktop broker.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/slchris/qubes-air/console/internal/desktop"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := desktop.CLI(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
