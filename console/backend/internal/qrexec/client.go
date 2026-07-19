// Package qrexec is a thin, injectable wrapper around Qubes qrexec: it shells
// out to `qrexec-client-vm <target> <service>`, feeding stdin and returning
// stdout. Argument allow-listing prevents command injection.
//
// This is the low-level primitive for cross-qube calls. It performs NO
// authorization — that lives in dom0 policy. Callers use it AFTER dom0 has
// authorized the call (e.g. the gRPC transport's remote-side QrexecInvoker runs
// a forward call post remote-dom0 re-check; the reverse handler runs a call
// that the local dom0 policy C has just ask-confirmed).
//
// The exec step is behind the Runner interface so tests can capture the call
// without a real qrexec-client-vm (mirrors the orchestrator terraform runner).
package qrexec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Runner executes a single qrexec call and returns its stdout. The default
// implementation shells out to qrexec-client-vm; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, target, service string, input []byte) ([]byte, error)
}

// Client calls qrexec services with a timeout and argument validation.
type Client struct {
	timeout time.Duration
	runner  Runner
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout overrides the per-call timeout (default 30s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithRunner injects a custom Runner (tests, or an alternate transport).
func WithRunner(r Runner) Option {
	return func(c *Client) {
		if r != nil {
			c.runner = r
		}
	}
}

// NewClient creates a qrexec client. By default it uses the real
// qrexec-client-vm runner and a 30s timeout.
func NewClient(opts ...Option) *Client {
	c := &Client{timeout: 30 * time.Second, runner: execRunner{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ValidArg reports whether an argument is a safe qrexec target/service token.
// Allow-list: [A-Za-z0-9._+-], non-empty. Prevents command injection.
func ValidArg(arg string) bool {
	for _, r := range arg {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '+') {
			return false
		}
	}
	return len(arg) > 0
}

// Call invokes a qrexec service: `qrexec-client-vm <target> <service>` with
// input on stdin, returning stdout. target and service are validated first.
//
// Call does NOT authorize — dom0 policy does. Invoke it only for calls dom0 has
// already authorized.
func (c *Client) Call(ctx context.Context, target, service string, input []byte) ([]byte, error) {
	if !ValidArg(target) {
		return nil, fmt.Errorf("invalid qrexec target: %q", target)
	}
	if !ValidArg(service) {
		return nil, fmt.Errorf("invalid qrexec service: %q", service)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.runner.Run(ctx, target, service, input)
}

// execRunner is the production Runner: it shells out to qrexec-client-vm.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, target, service string, input []byte) ([]byte, error) {
	// Args are validated by Client.Call before reaching here.
	cmd := exec.CommandContext(ctx, "qrexec-client-vm", target, service) // #nosec G204 -- validated args
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("qrexec call failed: %v, stderr: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
