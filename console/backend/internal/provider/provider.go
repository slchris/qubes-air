// Package provider defines the contract between the console's orchestrator and
// the infrastructure providers it drives directly.
//
// This is the seam that replaces Terraform. The console no longer renders a
// remote_qubes variable file, shells out to a binary, or reads a state file:
// an Adapter speaks each cloud's native API, and the SQLite qube_infra row is
// the single record of what exists.
//
// Adapters are selected by zone type. Every method must be idempotent: without
// a state file, a partially completed operation is retried from whatever
// identity was recorded, and the adapter is what makes "create what is missing,
// leave what exists" true.
package provider

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
)

// logWriterKey carries a writer adapters copy their progress to. The
// orchestrator injects it via provider.WithLogWriter so provider steps land in
// the same per-job log the rest of a provision writes to, rather than only in
// the console journal.
type logWriterKey struct{}

// WithLogWriter returns a context whose adapters should copy operation output
// to w. A nil writer is a no-op so callers need no nil check.
func WithLogWriter(ctx context.Context, w io.Writer) context.Context {
	if w == nil {
		return ctx
	}
	return context.WithValue(ctx, logWriterKey{}, w)
}

// LogWriterFrom returns the adapter log writer for this context, or nil.
func LogWriterFrom(ctx context.Context) io.Writer {
	w, _ := ctx.Value(logWriterKey{}).(io.Writer)
	return w
}

// Infra is the recorded identity of one qube's provider-side infrastructure.
//
// It replaces the terraform state file. The rule that makes it work is that it
// records allocated IDs BEFORE create requests via a required checkpoint.
// Observed volume details follow; ambiguous outcomes retain the reserved ID.
// Provider ownership checks are still required before adopting that ID.
type Infra struct {
	QubeID   string `json:"qube_id"`
	Provider string `json:"provider"` // models.ZoneType as string
	Node     string `json:"node,omitempty"`
	// StorageVMID is the small VM that owns the persistent data disk. It stays
	// for the life of the qube; only a purge removes it.
	StorageVMID int `json:"storage_vmid,omitempty"`
	// ComputeVMID is the ephemeral compute instance. Zero means none exists
	// right now (suspended or released).
	ComputeVMID int `json:"compute_vmid,omitempty"`
	// DataVolume identifies the persistent disk on its datastore, in whatever
	// form the provider uses to re-attach it on resume.
	DataVolume string `json:"data_volume,omitempty"`
	// IdentityVol is the pre-staged cloud-init identity document on shared
	// storage, when that delivery mode is used instead of per-node upload.
	IdentityVol string `json:"identity_vol,omitempty"`
	// ObservedState is the provider's most recent view: running / suspended /
	// absent. Empty means never observed.
	ObservedState string `json:"observed_state,omitempty"`
	// Protected is the explicit replacement for terraform's prevent_destroy.
	// DestroyStorage refuses while it is set, so releasing the data disk is a
	// deliberate act rather than an effect of the qube leaving a variable map.
	Protected  bool       `json:"protected"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// Observed is a provider's live view of a qube, returned by Describe.
type Observed struct {
	// State is the provider's own status word for the compute instance, not the
	// console's QubeStatus. Empty when no compute instance exists.
	State       string
	IPAddress   string
	Node        string
	StorageVMID int
	ComputeVMID int
	DataVolume  string
}

// Adapter drives one cloud provider directly.
//
// Implementations MUST treat qube and zone as the already-validated inputs the
// orchestrator hands them, and MUST NOT interpolate names into a shell. All
// methods are blocking; callers pass a context with a suitable timeout.
type Adapter interface {
	// VerifyDestroyed independently confirms absence of all recorded resources.
	VerifyDestroyed(ctx context.Context, q *models.Qube, in Infra) error
	// EnsureStorage creates the data-disk holder and its persistent disk if they
	// are absent, and returns their identity. It adopts in.StorageVMID when it
	// is already set, so a retry after a crash does not create a second disk.
	EnsureStorage(ctx context.Context, q *models.Qube, zone *models.Zone, in Infra) (Infra, error)

	// EnsureCompute creates the compute instance from the zone template, attaches
	// the persistent disk identified by in.DataVolume, delivers the agent
	// identity, and starts it. It adopts in.ComputeVMID when already set.
	EnsureCompute(ctx context.Context, q *models.Qube, zone *models.Zone, in Infra) (Infra, error)

	// StopCompute destroys the compute instance and leaves the persistent disk
	// untouched (suspend / release).
	StopCompute(ctx context.Context, q *models.Qube, in Infra) error

	// DestroyStorage destroys the persistent disk. It is destructive and
	// irreversible; the caller is responsible for clearing Protected first.
	DestroyStorage(ctx context.Context, q *models.Qube, in Infra) error

	// Describe returns the provider's current view of the qube. It must not
	// create or mutate anything.
	Describe(ctx context.Context, q *models.Qube, in Infra) (Observed, error)
}

// Constructor builds an Adapter for one zone, resolving that zone's credentials
// from the encrypted store.
type Constructor func(ctx context.Context, zone *models.Zone) (Adapter, error)

// ErrNoAdapter reports that no adapter is registered for a zone type.
//
// This is a hard failure by design: the console must never silently fall back
// to a different provider, because the symptom would be infrastructure created
// somewhere the operator did not intend.
type ErrNoAdapter struct {
	ZoneType models.ZoneType
	ZoneName string
}

func (e *ErrNoAdapter) Error() string {
	return fmt.Sprintf("no provider adapter registered for zone %q (type %q)", e.ZoneName, e.ZoneType)
}

// Registry maps a zone type to its adapter constructor.
type Registry struct {
	mu    sync.RWMutex
	ctors map[models.ZoneType]Constructor
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{ctors: make(map[models.ZoneType]Constructor)}
}

// Register adds a constructor for a zone type. Registering the same type twice
// is an error rather than a silent override: a second registration is almost
// certainly a wiring mistake, and silently keeping one of two adapters is how a
// provider ends up authenticating as something unexpected.
func (r *Registry) Register(zoneType models.ZoneType, ctor Constructor) error {
	if ctor == nil {
		return fmt.Errorf("provider: nil constructor for zone type %q", zoneType)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.ctors[zoneType]; dup {
		return fmt.Errorf("provider: adapter for zone type %q is already registered", zoneType)
	}
	r.ctors[zoneType] = ctor
	return nil
}

// Has reports whether a constructor is registered for a zone type.
func (r *Registry) Has(zoneType models.ZoneType) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ctors[zoneType] != nil
}

// For builds the adapter for a zone, or returns *ErrNoAdapter.
func (r *Registry) For(ctx context.Context, zone *models.Zone) (Adapter, error) {
	r.mu.RLock()
	ctor := r.ctors[zone.Type]
	r.mu.RUnlock()
	if ctor == nil {
		return nil, &ErrNoAdapter{ZoneType: zone.Type, ZoneName: zone.Name}
	}
	return ctor(ctx, zone)
}
