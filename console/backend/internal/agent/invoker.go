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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	// This is DEFENCE IN DEPTH, NOT A SECURITY BOUNDARY. The agent runs on an
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

// Invoke runs a qrexec service and returns its stdout.
//
// target is accepted for interface compatibility and recorded for logging, but
// carries no authority: on a single remote there is only one place a service
// can run, and treating a network-supplied name as a routing decision would be
// trusting the caller to address us correctly.
func (i *LocalInvoker) Invoke(ctx context.Context, target, service string, in []byte) ([]byte, error) {
	if !validServiceName(service) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidServiceName, service)
	}
	if len(i.Allowed) > 0 && !i.Allowed[baseService(service)] {
		return nil, fmt.Errorf("%w: %q", ErrServiceNotAllowed, service)
	}

	dir := i.ServiceDir
	if dir == "" {
		dir = DefaultServiceDir
	}
	// Qubes services may be invoked as "name+argument"; the implementation file
	// is the part before the '+', and the argument is passed to it.
	name, arg := splitServiceArg(service)
	path := filepath.Join(dir, name)

	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("%w: %q", ErrUnknownService, name)
	}
	if info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("%w: %q exists but is not executable", ErrUnknownService, name)
	}

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

	// A deliberately minimal environment. The agent's own environment may hold
	// credentials (its TLS key path, endpoints); a service script has no need
	// of them and inheriting wholesale is how such things leak into logs.
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"QUBESAIR_REMOTE_NAME=" + i.RemoteName,
		"QREXEC_REMOTE_DOMAIN=" + target,
		"QREXEC_SERVICE_FULL_NAME=" + service,
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

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

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("service %q timed out after %s", service, timeout)
		}
		// stderr is the service's own diagnostic and is the most useful thing an
		// operator can be shown, so it is surfaced rather than swallowed.
		return nil, fmt.Errorf("service %q failed: %v: %s",
			service, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() > maxResponseBytes {
		return nil, fmt.Errorf("%w: %q returned %d bytes", ErrResponseTooLarge, service, stdout.Len())
	}
	return stdout.Bytes(), nil
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
