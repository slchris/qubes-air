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
// without a real qrexec-client-vm (mirrors the orchestrator Executor).
package qrexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Runner executes a single qrexec call and returns its stdout. The default
// implementation shells out to qrexec-client-vm; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, target, service string, input []byte) ([]byte, error)
}

// Result is a completed qrexec call: the service's stdout, its stderr, and the
// exit code of the process that ran it.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// ResultRunner is an optional richer Runner. CallResult uses it when the
// injected runner implements it, so the stdout-only Runner (and every existing
// fake) keeps working unchanged.
type ResultRunner interface {
	RunResult(ctx context.Context, target, service string, input []byte) (Result, error)
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
		// staticcheck offers De Morgan's law here. Declining deliberately: the
		// current shape reads as "reject anything not in the allow-list", which
		// is the security property. The transformed version is a conjunction of
		// six negated range checks, where a single flipped comparison silently
		// widens what this accepts — and this function is the guard against
		// qrexec command injection.
		//nolint:staticcheck // QF1001: the allow-list reads correctly as written
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

// CallResult invokes a qrexec service and returns its stdout, stderr and exit
// code. Unlike Call, a non-zero exit is a RESULT, not an error: the caller
// decides whether it matters. An error means the call could not be made
// (bad arguments, timeout, qrexec-client-vm could not start).
func (c *Client) CallResult(ctx context.Context, target, service string, input []byte) (Result, error) {
	if !ValidArg(target) {
		return Result{}, fmt.Errorf("invalid qrexec target: %q", target)
	}
	if !ValidArg(service) {
		return Result{}, fmt.Errorf("invalid qrexec service: %q", service)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if rr, ok := c.runner.(ResultRunner); ok {
		return rr.RunResult(ctx, target, service, input)
	}
	out, err := c.runner.Run(ctx, target, service, input)
	if err != nil {
		return Result{}, err
	}
	return Result{Stdout: out}, nil
}

// execRunner is the production Runner: it shells out to qrexec-client-vm.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, target, service string, input []byte) ([]byte, error) {
	res, err := execRunner{}.RunResult(ctx, target, service, input)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("qrexec call failed: exit status %d, stderr: %s", res.ExitCode, res.Stderr)
	}
	return res.Stdout, nil
}

func (execRunner) RunResult(ctx context.Context, target, service string, input []byte) (Result, error) {
	// Args are validated by Client.Call/CallResult before reaching here.
	cmd := exec.CommandContext(ctx, "qrexec-client-vm", target, service) // #nosec G204 -- validated args
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	// A timeout or cancellation is a transport failure, not a command result:
	// surfacing it as exit code -1 would let a caller mistake it for the
	// command exiting.
	if ctx.Err() != nil {
		return res, fmt.Errorf("qrexec call failed: %w", ctx.Err())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.ExitCode = exit.ExitCode()
		return res, nil
	}
	return res, fmt.Errorf("qrexec call failed: %w", err)
}
