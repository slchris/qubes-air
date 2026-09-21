package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
)

// InfraStore persists the provider-side identity of a qube's infrastructure.
// repository.QubeInfraRepository satisfies it.
type InfraStore interface {
	Get(ctx context.Context, qubeID string) (*provider.Infra, error)
	Save(ctx context.Context, inf *provider.Infra) error
	Delete(ctx context.Context, qubeID string) error
}

// QubeZoneResolver maps a qube name to its row and zone. The Executor interface
// is name-keyed, so the native executor needs this lookup to know which provider
// to call and with which configuration.
type QubeZoneResolver interface {
	Resolve(ctx context.Context, qubeName string) (*models.Qube, *models.Zone, error)
}

// NativeExecutor errors.
var (
	// ErrInfraProtected refuses a destructive purge while the record is marked
	// protected. It replaces terraform's lifecycle.prevent_destroy.
	ErrInfraProtected = errors.New("qube infrastructure is protected; clear protection before destroying the data disk")
	// ErrNoInfrastructure means the qube has no recorded infrastructure, so the
	// requested operation has nothing to act on.
	ErrNoInfrastructure = errors.New("qube has no recorded infrastructure")
)

// NativeExecutor is the production Executor. It drives provider adapters
// directly — no terraform, no var-file, no state file.
//
// The database row (provider.Infra) is the single source of truth: every method
// checkpoints reserved IDs before provider creation, then records observed
// details. Retries retain those IDs and verify provider ownership.
type NativeExecutor struct {
	registry     *provider.Registry
	resolver     QubeZoneResolver
	store        InfraStore
	reachability ReachabilityProbe
	// probeTimeout/probeInterval bound the provision reachability wait. Zero
	// means the package defaults; tests set them small.
	probeTimeout  time.Duration
	probeInterval time.Duration
}

// ReachabilityProbe reports whether a qube's agent answers.
//
// Provision uses it as a gate. A provider that created a VM nobody can reach has
// not finished the job — it has produced a qube that reads "running" and is
// silently dead, which is exactly how an IP conflict or a failed agent install
// first shows up.
type ReachabilityProbe interface {
	// ProbeReachable returns nil when the agent answers, else why it did not.
	ProbeReachable(ctx context.Context, qube *models.Qube) error
}

// provisionProbeTimeout bounds the post-provision reachability wait. Cloud-init
// installs and starts the agent after the VM boots, so this covers a slow
// mirror while still failing a genuinely dead qube instead of hanging the job.
const (
	provisionProbeTimeout  = 4 * time.Minute
	provisionProbeInterval = 5 * time.Second
)

// WithReachability installs the probe Provision waits on. A nil probe disables
// the gate (tests, and providers that do not run an agent).
func (n *NativeExecutor) WithReachability(p ReachabilityProbe) *NativeExecutor {
	n.reachability = p
	return n
}

// NewNativeExecutor builds a NativeExecutor.
func NewNativeExecutor(registry *provider.Registry, resolver QubeZoneResolver, store InfraStore) *NativeExecutor {
	return &NativeExecutor{registry: registry, resolver: resolver, store: store}
}

// adapterFor validates the name, resolves the qube and zone, and selects the
// provider adapter. Every entry point funnels through here so validation and
// adapter selection cannot be skipped.
func (n *NativeExecutor) adapterFor(ctx context.Context, qubeName string) (*models.Qube, *models.Zone, provider.Adapter, error) {
	if !ValidQubeName(qubeName) {
		return nil, nil, nil, &ErrInvalidQubeName{Name: qubeName}
	}
	q, zone, err := n.resolver.Resolve(ctx, qubeName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve qube %q: %w", qubeName, err)
	}
	if q == nil {
		return nil, nil, nil, fmt.Errorf("resolve qube %q: not found", qubeName)
	}
	if zone == nil {
		return nil, nil, nil, fmt.Errorf("resolve qube %q: no zone", qubeName)
	}
	ad, err := n.registry.For(ctx, zone)
	if err != nil {
		return nil, nil, nil, err
	}
	return q, zone, ad, nil
}

// loadInfra returns the recorded infra, or nil when none exists.
func (n *NativeExecutor) loadInfra(ctx context.Context, q *models.Qube) (*provider.Infra, error) {
	inf, err := n.store.Get(ctx, q.ID)
	if err != nil {
		return nil, fmt.Errorf("load infra for %q: %w", q.Name, err)
	}
	return inf, nil
}

// save records an infra identity, stamping the owning qube id.
func (n *NativeExecutor) save(ctx context.Context, q *models.Qube, inf *provider.Infra) error {
	inf.QubeID = q.ID
	if err := n.store.Save(ctx, inf); err != nil {
		return fmt.Errorf("record infra for %q: %w", q.Name, err)
	}
	return nil
}

// currentInfra returns the recorded infra as a value, or a zero value when the
// qube has never had infrastructure. Adapters use it to adopt existing
// resources instead of creating duplicates.
func (n *NativeExecutor) currentInfra(ctx context.Context, q *models.Qube) (provider.Infra, error) {
	inf, err := n.loadInfra(ctx, q)
	if err != nil {
		return provider.Infra{}, err
	}
	if inf == nil {
		return provider.Infra{}, nil
	}
	return *inf, nil
}

// Provision creates the data-disk holder and the compute instance, recording
// each identity as soon as it exists.
func (n *NativeExecutor) Provision(ctx context.Context, qubeName string) error {
	q, zone, ad, err := n.adapterFor(ctx, qubeName)
	if err != nil {
		return err
	}
	if q.PurgeRequested {
		return errors.New("purge requested: compute creation is forbidden")
	}
	base, err := n.currentInfra(ctx, q)
	if err != nil {
		return err
	}
	if base.Provider == "" {
		base.Provider = string(zone.Type)
	}
	// A newly provisioned qube's data disk is protected from the moment it
	// exists; purging it is a separate, explicit act.
	base.Protected = true

	ctx = n.checkpointContext(ctx, q)
	storage, err := ad.EnsureStorage(ctx, q, zone, base)
	if err != nil {
		return fmt.Errorf("provision %q: ensure storage: %w", qubeName, err)
	}
	storage.Protected = true
	if err := n.save(ctx, q, &storage); err != nil {
		return err
	}

	compute, err := ad.EnsureCompute(ctx, q, zone, storage)
	if err != nil {
		return fmt.Errorf("provision %q: ensure compute: %w", qubeName, err)
	}
	if err := n.save(ctx, q, &compute); err != nil {
		return err
	}
	if n.reachability != nil {
		if err := n.awaitReachable(ctx, q, ad, compute); err != nil {
			return fmt.Errorf("provision %q: %w", qubeName, err)
		}
	}
	return nil
}

// awaitReachable polls until the agent answers or the wait runs out. A timeout
// is reported as a failed provision, not a warning: the qube is not usable, and
// the reason field on the last probe names what to look at next.
//
// The address is refreshed from the provider on every pass: the qube row the
// resolver returned predates the VM's boot, so its ip_address is empty until
// something else writes it, and probing an empty address would report
// no_address until the wait expired even on a perfectly healthy qube.
func (n *NativeExecutor) awaitReachable(ctx context.Context, q *models.Qube, ad provider.Adapter, in provider.Infra) error {
	timeout := n.probeTimeout
	if timeout <= 0 {
		timeout = provisionProbeTimeout
	}
	interval := n.probeInterval
	if interval <= 0 {
		interval = provisionProbeInterval
	}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if obs, err := ad.Describe(ctx, q, in); err == nil && obs.IPAddress != "" {
			q.IPAddress = obs.IPAddress
		}
		last = n.reachability.ProbeReachable(ctx, q)
		if last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("agent did not become reachable within %s (check the agent install, and whether the address is already used by another host): %w",
				timeout, last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent reachability wait canceled: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

// Resume rebuilds the compute instance and re-attaches the recorded data disk.
func (n *NativeExecutor) Resume(ctx context.Context, qubeName string) error {
	q, zone, ad, err := n.adapterFor(ctx, qubeName)
	if err != nil {
		return err
	}
	if q.PurgeRequested {
		return errors.New("purge requested: compute creation is forbidden")
	}
	inf, err := n.loadInfra(ctx, q)
	if err != nil {
		return err
	}
	if inf == nil || inf.DataVolume == "" {
		return n.Provision(ctx, qubeName)
	}
	ctx = n.checkpointContext(ctx, q)
	compute, err := ad.EnsureCompute(ctx, q, zone, *inf)
	if err != nil {
		return fmt.Errorf("resume %q: ensure compute: %w", qubeName, err)
	}
	if err := n.save(ctx, q, &compute); err != nil {
		return err
	}
	if n.reachability != nil {
		return n.awaitReachable(ctx, q, ad, compute)
	}
	return nil
}

// Suspend destroys the compute instance and keeps the data disk. Release uses
// the same provider work; the job action distinguishes the stored intent.
func (n *NativeExecutor) Suspend(ctx context.Context, qubeName string) error {
	q, _, ad, err := n.adapterFor(ctx, qubeName)
	if err != nil {
		return err
	}
	inf, err := n.loadInfra(ctx, q)
	if err != nil {
		return err
	}
	if inf == nil {
		return fmt.Errorf("%w: %q", ErrNoInfrastructure, qubeName)
	}
	if err := ad.StopCompute(ctx, q, *inf); err != nil {
		return fmt.Errorf("suspend %q: %w", qubeName, err)
	}
	inf.ComputeVMID = 0
	inf.ObservedState = "suspended"
	return n.save(ctx, q, inf)
}

// Destroy tears the qube down including its data disk. It refuses while the
// record is protected; releasing protection is a deliberate operator action.
func (n *NativeExecutor) Destroy(ctx context.Context, qubeName string) error {
	q, _, ad, err := n.adapterFor(ctx, qubeName)
	if err != nil {
		return err
	}
	inf, err := n.loadInfra(ctx, q)
	if err != nil {
		return err
	}
	// Nothing recorded means nothing to destroy; a purge of a qube that never
	// had infrastructure is a no-op, not an error.
	if inf == nil {
		return nil
	}
	if inf.Protected {
		return fmt.Errorf("%w: %q", ErrInfraProtected, qubeName)
	}

	ctx = provider.WithCheckpoint(ctx, func(ctx context.Context, observed provider.Infra) error {
		if err := n.save(ctx, q, &observed); err != nil {
			return err
		}
		*inf = observed
		return nil
	})

	if inf.ComputeVMID != 0 {
		if err := ad.StopCompute(ctx, q, *inf); err != nil {
			return fmt.Errorf("destroy %q: stop compute: %w", qubeName, err)
		}
		inf.ComputeVMID = 0
		if err := n.save(ctx, q, inf); err != nil {
			return err
		}
	}

	if err := ad.DestroyStorage(ctx, q, *inf); err != nil {
		return fmt.Errorf("destroy %q: destroy storage: %w", qubeName, err)
	}
	if err := ad.VerifyDestroyed(ctx, q, *inf); err != nil {
		return fmt.Errorf("destroy %q: verify resources: %w", qubeName, err)
	}
	if err := n.store.Delete(ctx, q.ID); err != nil {
		return fmt.Errorf("destroy %q: delete record: %w", qubeName, err)
	}
	return nil
}

// Status returns the provider's view of the compute instance, empty when none
// exists. It never mutates provider or database state.
func (n *NativeExecutor) Status(ctx context.Context, qubeName string) (string, error) {
	obs, err := n.describe(ctx, qubeName)
	if err != nil {
		return "", err
	}
	return obs.State, nil
}

// Address returns the qube's current IP address as the provider reports it.
//
// Deliberately NOT part of the Executor interface:
// consumers type-assert for it (see service.AgentAddressReader) and degrade when
// it is absent.
func (n *NativeExecutor) Address(ctx context.Context, qubeName string) (string, error) {
	obs, err := n.describe(ctx, qubeName)
	if err != nil {
		return "", err
	}
	return obs.IPAddress, nil
}

// describe runs the read-only provider query shared by Status and Address.
func (n *NativeExecutor) describe(ctx context.Context, qubeName string) (provider.Observed, error) {
	q, _, ad, err := n.adapterFor(ctx, qubeName)
	if err != nil {
		return provider.Observed{}, err
	}
	base, err := n.currentInfra(ctx, q)
	if err != nil {
		return provider.Observed{}, err
	}
	obs, err := ad.Describe(ctx, q, base)
	if err != nil {
		return provider.Observed{}, fmt.Errorf("describe %q: %w", qubeName, err)
	}
	return obs, nil
}

// checkpointContext persists reserved IDs before adapters send create requests.
func (n *NativeExecutor) checkpointContext(ctx context.Context, q *models.Qube) context.Context {
	return provider.WithCheckpoint(ctx, func(ctx context.Context, in provider.Infra) error {
		return n.save(ctx, q, &in)
	})
}
