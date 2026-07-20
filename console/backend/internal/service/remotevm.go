package service

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// RemoteVM registration: telling dom0 that a provisioned qube exists.
//
// The console provisions with terraform, so dom0 has no way to learn a qube
// exists — it writes terraform, not qvm-prefs. Until dom0 knows, no local qube
// can address the machine at all: RemoteVM is the addressing shell that makes
// `qrexec-client-vm remote-dev-1 <service>` resolvable, and without one the
// fleet is reachable only from the console itself.
//
// The channel is the dom0 qrexec service qubesair.RegisterRemoteVM, which
// accepts a fixed verb grammar and refuses any name outside `remote-*`. dom0
// policy allows only this qube to call it. Nothing here decides authorization;
// it just states what exists.

// QrexecCaller is the qrexec primitive this needs, narrowed to one method so
// tests can substitute it without a real qrexec-client-vm.
type QrexecCaller interface {
	Call(ctx context.Context, target, service string, input []byte) ([]byte, error)
}

// registerService is the dom0 service name; dom0Target is where it lives.
const (
	registerService = "qubesair.RegisterRemoteVM"
	dom0Target      = "dom0"
)

// RemoteVMRegistrar registers and deregisters RemoteVM addressing shells.
//
// Disabled by default. It requires the dom0 policy and service from
// mgmt.remotevm.register to be installed; with them absent every call fails,
// and a console that logged a registration failure after every provision would
// train an operator to ignore the log. Enable it once the channel exists.
type RemoteVMRegistrar struct {
	qrexec  QrexecCaller
	enabled bool
}

// NewRemoteVMRegistrar builds a registrar. A nil caller or enabled=false makes
// every method a no-op that reports why.
func NewRemoteVMRegistrar(qrexec QrexecCaller, enabled bool) *RemoteVMRegistrar {
	return &RemoteVMRegistrar{qrexec: qrexec, enabled: enabled}
}

// Enabled reports whether registration will actually be attempted.
func (r *RemoteVMRegistrar) Enabled() bool {
	return r != nil && r.enabled && r.qrexec != nil
}

// Register makes a provisioned qube addressable from local qubes.
//
// The local name and the remote name are deliberately the same string. That is
// not a simplification — the qube name is already the VM's hostname, the
// agent's QUBESAIR_REMOTE_NAME and the subject of its certificate (agent-<name>,
// see cloudinit.go). Introducing a fourth spelling here is how those four drift
// apart, and the drift only shows up as a call that resolves to the wrong
// machine.
func (r *RemoteVMRegistrar) Register(ctx context.Context, qubeName string) error {
	return r.call(ctx, "register", qubeName, qubeName)
}

// Deregister removes the addressing shell for a qube that is gone.
//
// Called when the fleet no longer contains the qube, not when its compute is
// merely parked: a suspended or released qube still exists and can be resumed,
// and dropping its registration would mean every resume needs a re-register to
// become addressable again.
func (r *RemoteVMRegistrar) Deregister(ctx context.Context, qubeName string) error {
	return r.call(ctx, "deregister", qubeName)
}

// call sends one request line and surfaces what dom0 said.
//
// The service reports failures on STDOUT (qrexec does not relay stderr), so a
// zero exit is not by itself success — the body has to be read. A refusal reads
// as "REFUSED"/"FAILED" there, and treating that as success would leave a qube
// recorded as addressable when dom0 never registered it.
func (r *RemoteVMRegistrar) call(ctx context.Context, verb string, args ...string) error {
	if !r.Enabled() {
		return fmt.Errorf("remotevm registration is disabled")
	}
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n") {
			return fmt.Errorf("invalid argument %q for %s", a, verb)
		}
	}
	line := verb + " " + strings.Join(args, " ") + "\n"

	out, err := r.qrexec.Call(ctx, dom0Target, registerService, []byte(line))
	if err != nil {
		return fmt.Errorf("%s %v: %w", verb, args, err)
	}
	body := strings.TrimSpace(string(out))
	if strings.Contains(body, "REFUSED") || strings.Contains(body, "FAILED") {
		return fmt.Errorf("%s %v refused by dom0: %s", verb, args, body)
	}
	return nil
}

// RegisterQuietly performs a registration whose failure must not fail the job
// that triggered it.
//
// A provision that reached a healthy agent produced a working machine. Failing
// the job because dom0 would not register an addressing shell would report the
// VM as broken when it is running fine — the same reasoning that keeps a silent
// agent from failing a completed apply. The gap is logged, loudly and with the
// remedy, because an unregistered qube is invisible to every local qube and
// nothing else will mention it.
func (r *RemoteVMRegistrar) RegisterQuietly(ctx context.Context, qubeName string) {
	if !r.Enabled() {
		return
	}
	if err := r.Register(ctx, qubeName); err != nil {
		log.Printf("remotevm: %q provisioned but NOT registered with dom0: %v "+
			"(it is reachable from the console but not addressable from local "+
			"qubes; check the mgmt.remotevm.register policy)", qubeName, err)
	}
}

// DeregisterQuietly is the teardown counterpart, with the same rationale: the
// qube is already gone, and a stale addressing shell is a cleanup problem, not
// a reason to report the removal as failed.
func (r *RemoteVMRegistrar) DeregisterQuietly(ctx context.Context, qubeName string) {
	if !r.Enabled() {
		return
	}
	if err := r.Deregister(ctx, qubeName); err != nil {
		log.Printf("remotevm: %q removed but its RemoteVM registration remains: %v "+
			"(drop it by hand: qubesair.RegisterRemoteVM deregister %s)",
			qubeName, err, qubeName)
	}
}
