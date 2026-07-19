// builtin.go — services implemented inside the agent process rather than by a
// script in ServiceDir.
//
// Every other service is a file: LocalInvoker.Invoke resolves the name to
// /etc/qubes-rpc/<name> and executes it. Certificate renewal cannot work that
// way. It manipulates the agent's OWN TLS state — the private key it holds, the
// certificate its listener presents — and a subprocess can do neither: handing
// a script the private key would defeat the point of generating it in memory,
// and no exec'd process can swap the certificate of a listener running in this
// one.

package agent

import (
	"context"
	"errors"
	"fmt"
)

// Builtin is a qrexec service implemented in-process.
//
// The signature matches LocalInvoker.Invoke minus the service name, so a
// builtin sees exactly what a script would: the calling target and the request
// body.
type Builtin func(ctx context.Context, target string, in []byte) ([]byte, error)

// Builtin registration errors.
var (
	// ErrBuiltinExists means the name was already registered. Registering twice
	// is a programming error, not a runtime condition: the second registration
	// would silently decide which implementation of certificate renewal runs.
	ErrBuiltinExists = errors.New("builtin service is already registered")
	// ErrBuiltinTakesNoArgument means a "name+arg" form was used for a builtin
	// that accepts none.
	ErrBuiltinTakesNoArgument = errors.New("builtin service takes no argument")
)

// RegisterBuiltin binds name to an in-process implementation.
//
// Builtins are resolved BEFORE ServiceDir and are not subject to the allowlist.
// Both are deliberate.
//
// ServiceDir is operator-writable, and on a host this project explicitly
// assumes is compromisable it is attacker-writable too. If a file there could
// shadow qubesair.CompleteRenewal, then dropping in a script would intercept
// renewal: the console would receive a plausible-looking reply, the real
// identity would never rotate, and the fleet would go dark on the day the old
// certificates expire with every health signal green until then. A path anyone
// can write to must not be able to override the mechanism that keeps the fleet
// authenticated.
//
// The allowlist is skipped for a related reason. It exists to stop a script
// dropped into the directory becoming callable by accident (see
// LocalInvoker.Allowed); a builtin cannot appear by accident, since only code
// compiled into this binary can register one. What the allowlist WOULD add is a
// way for an operator to disable renewal by forgetting to list it — a
// misconfiguration whose only symptom is certificates quietly not rotating,
// which is the exact silent failure renewal exists to remove.
func (i *LocalInvoker) RegisterBuiltin(name string, fn Builtin) error {
	if fn == nil {
		return fmt.Errorf("builtin %q: nil implementation", name)
	}
	if !validServiceName(name) || name != baseService(name) {
		return fmt.Errorf("%w: %q", ErrInvalidServiceName, name)
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.builtins[name]; exists {
		return fmt.Errorf("%w: %q", ErrBuiltinExists, name)
	}
	if i.builtins == nil {
		i.builtins = make(map[string]Builtin)
	}
	i.builtins[name] = fn
	return nil
}

// builtin returns the implementation registered for name, or nil.
func (i *LocalInvoker) builtin(name string) Builtin {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.builtins[name]
}

// IsBuiltin reports whether name is handled in-process.
//
// Exported so startup diagnostics do not warn that a builtin has no file in
// ServiceDir — it never will, and telling an operator to go install one would
// send them looking for a script that must not exist.
func (i *LocalInvoker) IsBuiltin(name string) bool {
	return i.builtin(baseService(name)) != nil
}
