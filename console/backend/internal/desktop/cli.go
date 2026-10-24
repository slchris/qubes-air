package desktop

import (
	"context"
	"fmt"
	"io"
)

// CLI exposes only menus, launch APP, and the fixed service's daemon entrypoint.
func CLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runCLI(ctx, args, stdout, stderr, Call, Run)
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer,
	request func(context.Context, Request) Response, daemon func(context.Context) error) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, _ = fmt.Fprintln(stdout, "usage: qubes-air-desktop menus | launch APP | daemon")
		return 0
	}
	if len(args) == 1 && args[0] == "daemon" {
		if err := daemon(ctx); err != nil {
			_, _ = fmt.Fprintln(stderr, "qubes-air-desktop: desktop session unavailable; check unit user and runtime directories")
			return 69
		}
		return 0
	}
	r, ok := cliRequest(args)
	if !ok {
		_, _ = fmt.Fprintln(stderr, failure(64).Error)
		return 64
	}
	reply := request(ctx, r)
	if !reply.OK {
		_, _ = fmt.Fprintln(stderr, reply.Error)
		return reply.Code
	}
	if r.Operation == "menus" {
		_, _ = io.WriteString(stdout, reply.Menu)
	} else {
		_, _ = fmt.Fprintf(stdout, "qubes.StartApp: accepted '%s' on %s\n", reply.App, reply.Display)
	}
	return 0
}

func cliRequest(args []string) (Request, bool) {
	if len(args) == 1 && args[0] == "menus" {
		return Request{Operation: "menus"}, true
	}
	if len(args) == 2 && args[0] == operationLaunch && ValidAppID(args[1]) {
		return Request{Operation: operationLaunch, App: args[1]}, true
	}
	return Request{}, false
}
