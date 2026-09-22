// Package agent implements the RemoteVM agent: the process that runs on a
// non-Qubes remote and gives it qrexec semantics without Xen vchan.
//
// The remote cannot run qrexec-client-vm. That binary ships in
// qubes-core-agent-linux and needs libvchan, qubesdb and a dom0 qrexec-daemon at
// runtime; vchan is a single-host Xen shared-memory primitive with no meaning
// across machines, and a KVM guest has none of the three. See
// docs/remote-agent-design.md.
//
// What this package provides instead is an executor for the same service
// convention — /etc/qubes-rpc/<service> — reached over the existing gRPC tunnel.
package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/transport"
)

// DefaultServiceDir is where qrexec service implementations live, matching the
// Qubes convention so an existing service script is portable here unchanged.
const DefaultServiceDir = "/etc/qubes-rpc"

// DefaultCallTimeout bounds a single service execution.
const DefaultCallTimeout = 2 * time.Minute

// maxResponseBytes caps what one service may return, so a runaway script cannot
// exhaust the agent's memory.
const maxResponseBytes = 16 << 20 // 16 MiB

// Invoker errors.
var (
	// ErrUnknownService means no implementation exists for the requested name.
	ErrUnknownService = errors.New("no such qrexec service")
	// ErrServiceNotAllowed means the service exists but is not permitted.
	ErrServiceNotAllowed = errors.New("qrexec service is not allowed on this agent")
	// ErrInvalidServiceName means the name could not be a service.
	ErrInvalidServiceName = errors.New("invalid qrexec service name")
	// ErrResponseTooLarge means the service produced more than the cap.
	ErrResponseTooLarge = errors.New("service response exceeded the size limit")
)

// LocalInvoker executes qrexec services implemented on this host.
//
// It satisfies the transport's QrexecInvoker, so the remote server runs local
// scripts where a Qubes host would have shelled out to qrexec-client-vm.
type LocalInvoker struct {
	// ServiceDir holds the service implementations (default DefaultServiceDir).
	ServiceDir string
	// Allowed, when non-empty, restricts which services may run.
	//
	// This is DEFENSE IN DEPTH, NOT A SECURITY BOUNDARY. The agent runs on an
	// untrusted host: whoever compromises it can replace this binary and skip
	// the check entirely. Authorization belongs to the local dom0 policy, which
	// decided before the call ever left the trusted side. What this does buy is
	// protection against misconfiguration — a service script dropped into the
	// directory does not become callable by accident.
	Allowed map[string]bool
	// Timeout bounds one execution (default DefaultCallTimeout).
	Timeout time.Duration
	// RemoteName is exported to services as QUBESAIR_REMOTE_NAME, aligning with
	// the Qubes RemoteVM remote_name property.
	RemoteName string

	// mu guards builtins. Registration happens at startup, but the map is read
	// on every call from the gRPC server's per-request goroutines, and an
	// unsynchronized map read against a late registration is a data race with
	// no upper bound on what it corrupts.
	mu sync.RWMutex
	// builtins are services handled in-process; see builtin.go for why they
	// cannot be files and why nothing in ServiceDir may shadow them.
	builtins map[string]Builtin
}

// NewLocalInvoker builds an invoker over the standard service directory.
func NewLocalInvoker(remoteName string, allowed []string) *LocalInvoker {
	set := make(map[string]bool, len(allowed))
	for _, s := range allowed {
		set[s] = true
	}
	return &LocalInvoker{
		ServiceDir: DefaultServiceDir,
		Allowed:    set,
		Timeout:    DefaultCallTimeout,
		RemoteName: remoteName,
	}
}

// validServiceName reports whether name is safe to resolve to a file.
//
// The name arrives over the network and becomes a path element, so it is
// restricted to a conservative character set and must contain no separator.
// Rejecting "..", "/" and "" is what stops a request escaping ServiceDir.
// A character allow-list: each branch is one permitted class. gocyclo counts
// the classes, but there is no decomposition that makes this safer to read —
// splitting it would spread the guard against qrexec injection across files.
//
//nolint:gocyclo // an allow-list, not a decision tree
func validServiceName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !alnum && c != '.' && c != '-' && c != '_' && c != '+' {
			return false
		}
	}
	return true
}

// Invoke runs a qrexec service and returns its stdout, stderr and exit code.
//
// A non-zero exit is part of the RESULT, not an error: the caller decides
// whether it matters. err is reserved for the call not being made (unknown or
// disallowed service, timeout, output over the cap). This is what removes the
// old need for a service script to swallow its exit code and append a text
// trailer — the structure now carries it.
//
// target is accepted for interface compatibility and recorded for logging, but
// carries no authority: on a single remote there is only one place a service
// can run, and treating a network-supplied name as a routing decision would be
// trusting the caller to address us correctly.
func (i *LocalInvoker) Invoke(ctx context.Context, target, service string, in []byte) (result transport.Result, err error) {
	started := time.Now()
	defer func() { logInvocation(target, service, started, result.ExitCode, &err) }()

	if !validServiceName(service) {
		return transport.Result{}, fmt.Errorf("%w: %q", ErrInvalidServiceName, service)
	}

	// Qubes services may be invoked as "name+argument"; the implementation file
	// is the part before the '+', and the argument is passed to it.
	name, arg := splitServiceArg(service)

	// Builtins are resolved first, on the BASE name, and before the allowlist —
	// see RegisterBuiltin. Matching on the base name is what closes the gap:
	// dispatching "qubesair.CompleteRenewal+x" down the file path would let a
	// script in ServiceDir serve a request the builtin was supposed to answer.
	if fn := i.builtin(name); fn != nil {
		if arg != "" {
			return transport.Result{}, fmt.Errorf("%w: %q", ErrBuiltinTakesNoArgument, service)
		}
		out, err := fn(ctx, target, in)
		if err != nil {
			return transport.Result{}, err
		}
		return transport.Result{Stdout: out}, nil
	}

	if len(i.Allowed) > 0 && !i.Allowed[name] {
		return transport.Result{}, fmt.Errorf("%w: %q", ErrServiceNotAllowed, service)
	}

	dir := i.ServiceDir
	if dir == "" {
		dir = DefaultServiceDir
	}
	path := filepath.Join(dir, name)

	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return transport.Result{}, fmt.Errorf("%w: %q", ErrUnknownService, name)
	}
	if info.Mode()&0o111 == 0 {
		return transport.Result{}, fmt.Errorf("%w: %q exists but is not executable", ErrUnknownService, name)
	}

	return i.run(ctx, target, service, arg, path, in)
}

// run executes a resolved service file and maps its termination to a structured
// result. Split out of Invoke so the name/builtin/allowlist dispatch and the
// process handling are not one long function.
func (i *LocalInvoker) run(ctx context.Context, target, service, arg, path string, in []byte) (transport.Result, error) {
	timeout := i.Timeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- path is ServiceDir joined with a name validated above to
	// contain no separator and no "..", so it cannot escape the directory.
	cmd := exec.CommandContext(ctx, path)
	if arg != "" {
		cmd.Args = append(cmd.Args, arg)
	}
	cmd.Stdin = bytes.NewReader(in)
	cmd.Env = i.serviceEnv(target, service)

	// Bounded buffers: a service that floods stdout is stopped AT the cap, not
	// buffered in full and rejected afterwards. stdout overflow aborts the call;
	// stderr is truncated silently (it is only a diagnostic) so a chatty service
	// cannot exhaust the agent either way.
	stdout := &capWriter{limit: maxResponseBytes, abortOnOverflow: true}
	stderr := &capWriter{limit: maxResponseBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Without WaitDelay the timeout above does not actually bound the call.
	//
	// Stdout/Stderr are buffers rather than *os.File, so exec creates an OS pipe
	// and a copying goroutine, and Wait blocks until every writer closes. The
	// context kills only the DIRECT child; any grandchild it left behind
	// inherits the pipe's write end and holds it open. A service that
	// backgrounds anything therefore pins this call for the grandchild's
	// lifetime — measured at 30s against a 200ms timeout — and a hostile or
	// merely careless service could hold an agent worker indefinitely.
	//
	// WaitDelay bounds the drain: after cancellation, wait this long for I/O to
	// finish, then force the pipes closed and return. The deadline is what
	// decides the outcome; this only stops the cleanup from outliving it.
	cmd.WaitDelay = 2 * time.Second

	runErr := cmd.Run()
	if stdout.overflow {
		return transport.Result{}, fmt.Errorf("%w: %q produced more than %d bytes", ErrResponseTooLarge, service, maxResponseBytes)
	}
	res := transport.Result{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return transport.Result{}, fmt.Errorf("service %q timed out after %s: %w", service, timeout, ctx.Err())
		}
		if ctx.Err() != nil {
			return transport.Result{}, fmt.Errorf("service %q canceled: %w", service, ctx.Err())
		}
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			// The service ran and reported failure; that is a result the caller
			// may act on, not a failure to run it.
			res.ExitCode = exit.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("service %q failed: %w", service, runErr)
	}
	return res, nil
}

// capWriter is an io.Writer that keeps at most limit bytes. With
// abortOnOverflow it makes the write fail, which stops exec's copy goroutine and
// surfaces as the command's error; otherwise it truncates and reports the write
// as complete, so a chatty stream cannot grow memory without failing the call.
type capWriter struct {
	buf             bytes.Buffer
	limit           int
	abortOnOverflow bool
	overflow        bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.limit <= 0 || w.buf.Len()+len(p) <= w.limit {
		return w.buf.Write(p)
	}
	if room := w.limit - w.buf.Len(); room > 0 {
		_, _ = w.buf.Write(p[:room])
	}
	w.overflow = true
	if w.abortOnOverflow {
		return 0, ErrResponseTooLarge
	}
	return len(p), nil
}

// serviceEnv is the deliberately minimal environment a service script gets. The
// agent's own environment may hold credentials (its TLS key path, endpoints); a
// service has no need of them and inheriting wholesale is how such things leak
// into logs. The only pass-throughs are the opt-in gates for the privileged
// services, read from agent.env so widening Exec/FileCopy is a reviewed config
// change rather than an implicit default.
func (i *LocalInvoker) serviceEnv(target, service string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"QUBESAIR_REMOTE_NAME=" + i.RemoteName,
		"QREXEC_REMOTE_DOMAIN=" + target,
		"QREXEC_SERVICE_FULL_NAME=" + service,
	}
	for _, k := range []string{"QUBESAIR_EXEC_ALLOW", "QUBESAIR_FILECOPY_ROOTS"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// logInvocation records one service invocation: target, service, outcome, the
// command's exit code and duration. The caller identity is recorded by the
// transport layer when it accepts the client certificate, so together the two
// lines answer "who asked what, and did it work". A non-zero exit is reported
// alongside outcome=ok: the call ran, and the command it ran failed — two
// different things the old text-trailer protocol could not separate.
func logInvocation(target, service string, started time.Time, exitCode int, err *error) {
	outcome := "ok"
	if err != nil && *err != nil {
		outcome = "error"
	}
	log.Printf("agent invoke: target=%q service=%q outcome=%s exit=%d duration=%s",
		target, service, outcome, exitCode, time.Since(started).Round(time.Millisecond))
}

// splitServiceArg separates "service+argument" into its parts.
func splitServiceArg(service string) (name, arg string) {
	if n, a, ok := strings.Cut(service, "+"); ok {
		return n, a
	}
	return service, ""
}

// baseService returns the service name without its argument, which is what an
// allowlist entry matches — otherwise every possible argument would need
// listing separately.
func baseService(service string) string {
	name, _ := splitServiceArg(service)
	return name
}
