// agentunlock.go — pushes a qube's data-disk key to its agent to open the
// encrypted /data, migrating disks that predate per-qube keys.
//
// This runs after bootstrap succeeds, so the agent already holds its real
// identity and the channel is VERIFIED (the agent's certificate CN is pinned to
// agent-<qube>, exactly like the prober). The bootstrap dial deliberately skips
// server verification because a bootstrapping agent has no certificate yet; a
// SECRET must never travel over that unverified channel, so the unlock uses the
// prober's verified path instead.
//
// Migration: a disk formatted under the legacy master-derived scheme is rekeyed
// to the qube's own data key (DEK) the first time it is opened after this code
// ships. The old key is only ever used on this path, over the same verified
// channel, and the rekey removes it from the container — after that the master
// secret is no longer needed for that qube.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

const (
	unlockRelayName    = "console-unlock"
	unlockCertLifetime = 5 * time.Minute
	unlockDataService  = "qubesair.UnlockData"
	rekeyDataService   = "qubesair.RekeyData"

	// unlockReasonWrongKey is what the agent reports when the pushed key does
	// not open an existing LUKS container. It is the ONLY result that triggers
	// migration: any other refusal (no disk, non-LUKS filesystem, mount failure)
	// must not be papered over by rekeying.
	unlockReasonWrongKey = "wrong_key"

	// DefaultDataUnlockTimeout bounds one unlock attempt: dial, verified
	// handshake, and the agent's luksFormat/luksOpen/mkfs/mount. Generous because
	// first-boot formatting of a fresh container is the slow case, still short
	// enough that a wedged agent does not pin the bootstrap sweep. Migration may
	// make one extra call on the same tunnel; the budget covers both.
	DefaultDataUnlockTimeout = 60 * time.Second
)

// DataKeyProvider supplies a qube's data key and its migration inputs.
type DataKeyProvider interface {
	// KeyFor returns the qube's own stored DEK; found is false when it has
	// none, which marks the disk as one that needs migration.
	KeyFor(ctx context.Context, qubeID string) (key string, found bool, err error)
	// EnsureDataKey mints and stores a DEK if the qube has none.
	EnsureDataKey(ctx context.Context, qubeID string) (string, error)
	// LegacyKeyFor derives the old master-based key. It never mints a master
	// and errors when the master is absent.
	LegacyKeyFor(ctx context.Context, qubeID string) (string, error)
}

// migrationMarker records, outside the process, that a rekey may not have
// removed the legacy keyslot. DataKeyManager implements it; a key provider that
// does not simply loses the retry (the failure is still logged).
type migrationMarker interface {
	MigrationPending(ctx context.Context, qubeID string) (bool, error)
	MarkMigrationPending(ctx context.Context, qubeID string) error
	ClearMigrationPending(ctx context.Context, qubeID string) error
}

// AgentDataUnlocker opens a qube's encrypted data disk by loading its key and
// pushing it to qubesair.UnlockData over verified mTLS. The key is held only in
// the request and on the agent's RAM. Idempotent on the agent, so calling it
// after every bootstrap is safe.
type AgentDataUnlocker struct {
	ca      CAProvider
	keys    DataKeyProvider
	marker  migrationMarker
	dialer  AgentDialer
	timeout time.Duration
}

// NewAgentDataUnlocker builds an unlocker. A nil return of ca or keys makes
// Unlock a no-op error, which is how a console with encryption disabled behaves.
func NewAgentDataUnlocker(ca CAProvider, keys DataKeyProvider, agentListen string, timeout time.Duration) *AgentDataUnlocker {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	u := &AgentDataUnlocker{
		ca:      ca,
		keys:    keys,
		dialer:  NewDirectDialer(agentListen),
		timeout: timeout,
	}
	if m, ok := keys.(migrationMarker); ok {
		u.marker = m
	}
	return u
}

// UnlockResult is the agent's answer: whether /data is now open, why not, and
// whether the disk had to be migrated to its own key first.
type UnlockResult struct {
	Unlocked bool
	Reason   string
	Detail   string
	Migrated bool
}

// Unlock loads the qube's data key and asks its agent to open /data. A disk
// still keyed by the legacy master is migrated to the qube's DEK first, on the
// same verified tunnel.
func (u *AgentDataUnlocker) Unlock(ctx context.Context, qube *models.Qube) (UnlockResult, error) {
	if u == nil || u.ca == nil || u.keys == nil {
		return UnlockResult{}, errors.New("no data unlocker configured")
	}
	if qube == nil {
		return UnlockResult{}, errors.New("no qube given")
	}
	if strings.TrimSpace(qube.IPAddress) == "" {
		return UnlockResult{}, fmt.Errorf("qube %q has no address to unlock", qube.Name)
	}

	// Load first: a credential-store failure means there is no point dialing the
	// agent to hand it nothing.
	key, found, err := u.keys.KeyFor(ctx, qube.ID)
	if err != nil {
		return UnlockResult{}, fmt.Errorf("load data key for %q: %w", qube.Name, err)
	}
	pending, err := u.migrationPending(ctx, qube.ID)
	if err != nil {
		return UnlockResult{}, fmt.Errorf("read migration state for %q: %w", qube.Name, err)
	}

	ctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	return u.withAgent(ctx, qube, func(t agentTunnel) (UnlockResult, error) {
		return u.unlockOn(ctx, t, qube, key, found, pending)
	})
}

// unlockOn is the decision table: open with the stored key, migrate when the
// disk is (or may be) still keyed by the legacy master, and never replace a
// concrete agent refusal with a rekey.
func (u *AgentDataUnlocker) unlockOn(
	ctx context.Context, t agentTunnel, qube *models.Qube, key string, found, pending bool,
) (UnlockResult, error) {
	if !found {
		// No stored key: a disk formatted before per-qube keys existed. It can
		// only be opened with the legacy derivation, and it must be rekeyed as
		// part of opening it.
		return u.migrate(ctx, t, qube, "", false)
	}

	res, err := u.unlockWith(ctx, t, qube, key)
	if err != nil {
		return UnlockResult{}, err
	}
	if res.Unlocked && !pending {
		return res, nil
	}
	if res.Unlocked {
		// The disk opened with its DEK, but a previous rekey did not verify
		// that the legacy keyslot was gone. Finish it before treating the qube
		// as migrated: while the old slot exists, the master secret still opens
		// this disk.
		return u.migrate(ctx, t, qube, key, true)
	}
	if res.Reason != unlockReasonWrongKey {
		return res, nil
	}
	// The stored DEK does not open the container: a previous migration stored
	// the key but the rekey did not take effect (it may have failed, or the
	// process died first). Rekey to the SAME key, so a retry never orphans a
	// key that is already stored.
	return u.migrate(ctx, t, qube, key, true)
}

// agentTunnel is the subset of the relay client the unlocker uses. It is an
// interface so the migration decision can be tested without TLS.
type agentTunnel interface {
	Call(ctx context.Context, target, service string, in []byte) ([]byte, error)
}

// migrationPending reports the persisted migration state, or false when the key
// provider cannot persist one.
func (u *AgentDataUnlocker) migrationPending(ctx context.Context, qubeID string) (bool, error) {
	if u.marker == nil {
		return false, nil
	}
	return u.marker.MigrationPending(ctx, qubeID)
}

func (u *AgentDataUnlocker) markMigrationPending(ctx context.Context, qubeID string) error {
	if u.marker == nil {
		return nil
	}
	return u.marker.MarkMigrationPending(ctx, qubeID)
}

func (u *AgentDataUnlocker) clearMigrationPending(ctx context.Context, qubeID string) error {
	if u.marker == nil {
		return nil
	}
	return u.marker.ClearMigrationPending(ctx, qubeID)
}

// withAgent mints a console client certificate, opens the verified tunnel and
// runs fn on it, so unlock and migration share one connection.
func (u *AgentDataUnlocker) withAgent(
	ctx context.Context, qube *models.Qube, fn func(agentTunnel) (UnlockResult, error),
) (UnlockResult, error) {
	ca, err := u.ca.CA(ctx)
	if err != nil {
		return UnlockResult{}, fmt.Errorf("no usable CA to reach %q: %w", qube.Name, err)
	}
	bundle, err := ca.IssueAgentCert(unlockRelayName, unlockCertLifetime)
	if err != nil {
		return UnlockResult{}, fmt.Errorf("mint unlock client certificate: %w", err)
	}
	// Pin the peer to agent-<qube>: key material must reach THIS qube's agent and
	// no impostor at its address. Same binding the prober enforces.
	tlsCfg, err := probeTLSConfig(bundle, AgentCommonName(qube.Name))
	if err != nil {
		return UnlockResult{}, fmt.Errorf("unlock client certificate unusable: %w", err)
	}

	addr := u.dialer.Address(qube)
	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      unlockRelayName,
		RemoteName:     qube.Name,
		Dialer:         dialFuncFor(u.dialer, qube),
		ReconnectMin:   20 * time.Millisecond,
		ReconnectMax:   200 * time.Millisecond,
		TLS:            tlsCfg.Clone(),
	}, nil)
	go func() { _ = cli.Start(ctx) }()

	return fn(cli)
}

// unlockWith pushes one key to qubesair.UnlockData and parses its reply.
func (u *AgentDataUnlocker) unlockWith(
	ctx context.Context, t agentTunnel, qube *models.Qube, key string,
) (UnlockResult, error) {
	out, err := callWhenConnected(ctx, t, qube.Name, unlockDataService, []byte(key))
	if err != nil {
		return UnlockResult{}, fmt.Errorf("call %s on %q: %w", unlockDataService, qube.Name, err)
	}
	var reply struct {
		Unlocked bool   `json:"unlocked"`
		Reason   string `json:"reason"`
		Detail   string `json:"detail"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return UnlockResult{}, fmt.Errorf("unparseable %s reply from %q: %v (%q)",
			unlockDataService, qube.Name, err, strings.TrimSpace(string(out)))
	}
	return UnlockResult{Unlocked: reply.Unlocked, Reason: reply.Reason, Detail: reply.Detail}, nil
}

// migrate rekeys a legacy disk to the qube's own key and then opens it.
//
// Ordering is the whole safety argument: the new key is stored (when the qube
// had none) BEFORE the rekey request, and the rekey is an atomic cryptsetup
// key change that leaves the old key valid if it fails. There is no sequence in
// which a crash locks the data away, and a retry reuses the stored key rather
// than minting another one.
func (u *AgentDataUnlocker) migrate(
	ctx context.Context, t agentTunnel, qube *models.Qube, storedKey string, found bool,
) (UnlockResult, error) {
	oldKey, err := u.keys.LegacyKeyFor(ctx, qube.ID)
	if err != nil {
		return UnlockResult{}, fmt.Errorf(
			"qube %q needs its legacy data key to migrate, but it is unavailable: %w", qube.Name, err)
	}

	newKey := storedKey
	if !found {
		newKey, err = u.keys.EnsureDataKey(ctx, qube.ID)
		if err != nil {
			return UnlockResult{}, fmt.Errorf("mint data key to migrate %q: %w", qube.Name, err)
		}
	}
	// Record the intent before touching the container: if the process dies
	// during the rekey, the next unlock re-verifies and finishes the removal
	// instead of assuming the legacy slot is gone.
	if err := u.markMigrationPending(ctx, qube.ID); err != nil {
		return UnlockResult{}, fmt.Errorf("record migration state for %q: %w", qube.Name, err)
	}

	payload, err := json.Marshal(struct {
		Old string `json:"old"`
		New string `json:"new"`
	}{Old: oldKey, New: newKey})
	if err != nil {
		return UnlockResult{}, fmt.Errorf("encode rekey request for %q: %w", qube.Name, err)
	}

	out, err := callWhenConnected(ctx, t, qube.Name, rekeyDataService, payload)
	if err != nil {
		return UnlockResult{}, fmt.Errorf("call %s on %q: %w", rekeyDataService, qube.Name, err)
	}
	var reply struct {
		Rekeyed       bool   `json:"rekeyed"`
		OldKeyRemoved bool   `json:"old_key_removed"`
		Reason        string `json:"reason"`
		Detail        string `json:"detail"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return UnlockResult{}, fmt.Errorf("unparseable %s reply from %q: %v (%q)",
			rekeyDataService, qube.Name, err, strings.TrimSpace(string(out)))
	}
	if !reply.Rekeyed {
		log.Printf("unlock: qube %q data-disk migration refused (%s): %s; data stays encrypted",
			qube.Name, reply.Reason, reply.Detail)
		return UnlockResult{Reason: reply.Reason, Detail: "migration failed: " + reply.Detail}, nil
	}
	if !reply.OldKeyRemoved {
		// Usable for the data, but the legacy key must not stay valid: while
		// that slot exists, the master secret still opens this disk. The marker
		// set above keeps the qube "pending" so every later unlock re-verifies
		// and retries the removal.
		log.Printf("pki: qube %q was rekeyed to its own data key, but the legacy keyslot could not be removed: "+
			"%s; the next unlock retries removal", qube.Name, reply.Detail)
	} else {
		if err := u.clearMigrationPending(ctx, qube.ID); err != nil {
			return UnlockResult{}, fmt.Errorf("clear migration state for %q: %w", qube.Name, err)
		}
		log.Printf("pki: migrated qube %q data disk to its own key and removed the legacy keyslot", qube.Name)
	}

	res, err := u.unlockWith(ctx, t, qube, newKey)
	if err != nil {
		return UnlockResult{}, err
	}
	res.Migrated = true
	return res, nil
}

// callWhenConnected calls a service, retrying only while the tunnel is still
// coming up (Call reports ErrNotConnected until Start's first handshake lands).
// Any other error is returned immediately. Mirrors the prober's ping loop.
func callWhenConnected(
	ctx context.Context, t agentTunnel, target, service string, in []byte,
) ([]byte, error) {
	const retryEvery = 25 * time.Millisecond
	var lastErr error
	for {
		out, err := t.Call(ctx, target, service, in)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !errors.Is(err, transportgrpc.ErrNotConnected) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("tunnel never established: %w", lastErr)
		case <-time.After(retryEvery):
		}
	}
}

// UnlockData is the callback shape the bootstrap monitor's AfterBootstrap hook
// wants. It unlocks only qubes whose spec asks for it, logs the outcome, and
// never returns an error the sweep would have to handle — a failed unlock leaves
// the data safe (still encrypted) and is retried on the next resume.
func (u *AgentDataUnlocker) UnlockData(ctx context.Context, qube *models.Qube) {
	if qube == nil || !qube.Spec.EncryptsData() {
		return
	}
	res, err := u.Unlock(ctx, qube)
	switch {
	case err != nil:
		log.Printf("unlock: qube %q data disk NOT opened: %v (data stays encrypted; retried on next resume)", qube.Name, err)
	case !res.Unlocked:
		log.Printf("unlock: qube %q data disk NOT opened: %s (data stays encrypted; retried on next resume)", qube.Name, res.Detail)
	case res.Migrated:
		log.Printf("unlock: qube %q data disk migrated to its own key and opened (%s)", qube.Name, res.Detail)
	default:
		log.Printf("unlock: qube %q data disk opened and mounted (%s)", qube.Name, res.Detail)
	}
}
