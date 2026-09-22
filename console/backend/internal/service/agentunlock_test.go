package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKeys is an in-memory DataKeyProvider plus migration marker.
type fakeKeys struct {
	stored    map[string]string
	pending   map[string]bool
	legacyErr error

	legacyCalls int
	ensureCalls int
	markCalls   int
	clearCalls  int
}

func newFakeKeys() *fakeKeys {
	return &fakeKeys{stored: map[string]string{}, pending: map[string]bool{}}
}

func (f *fakeKeys) KeyFor(_ context.Context, qubeID string) (string, bool, error) {
	key, ok := f.stored[qubeID]
	return key, ok, nil
}

func (f *fakeKeys) EnsureDataKey(_ context.Context, qubeID string) (string, error) {
	f.ensureCalls++
	if key, ok := f.stored[qubeID]; ok {
		return key, nil
	}
	key := "minted-dek-" + qubeID
	f.stored[qubeID] = key
	return key, nil
}

func (f *fakeKeys) LegacyKeyFor(_ context.Context, qubeID string) (string, error) {
	f.legacyCalls++
	if f.legacyErr != nil {
		return "", f.legacyErr
	}
	return "legacy-" + qubeID, nil
}

func (f *fakeKeys) MigrationPending(_ context.Context, qubeID string) (bool, error) {
	return f.pending[qubeID], nil
}

func (f *fakeKeys) MarkMigrationPending(_ context.Context, qubeID string) error {
	f.markCalls++
	f.pending[qubeID] = true
	return nil
}

func (f *fakeKeys) ClearMigrationPending(_ context.Context, qubeID string) error {
	f.clearCalls++
	delete(f.pending, qubeID)
	return nil
}

// fakeTunnel serves scripted UnlockData and RekeyData replies and records what
// the unloader asked for.
type fakeTunnel struct {
	unlock func(key string) string
	rekey  func(payload string) string

	unlockKeys []string
	rekeyCalls int
}

func (f *fakeTunnel) Call(_ context.Context, _ string, service string, in []byte) ([]byte, error) {
	switch service {
	case unlockDataService:
		f.unlockKeys = append(f.unlockKeys, string(in))
		return []byte(f.unlock(string(in))), nil
	case rekeyDataService:
		f.rekeyCalls++
		return []byte(f.rekey(string(in))), nil
	default:
		return nil, fmt.Errorf("unexpected service %q", service)
	}
}

// unreachedCA fails if consulted; the tests that use it must never reach a dial.
type unreachedCA struct{ t *testing.T }

func (c unreachedCA) CA(context.Context) (*pki.CA, error) {
	c.t.Helper()
	c.t.Fatal("CA must not be consulted on this path")
	return nil, errors.New("unreachable")
}

func unlockReply(unlocked bool, reason string) string {
	payload, _ := json.Marshal(map[string]any{"unlocked": unlocked, "reason": reason, "detail": "test"})
	return string(payload)
}

func TestUnlockOnOpensWithStoredKeyWithoutMigration(t *testing.T) {
	keys := newFakeKeys()
	keys.stored["q1"] = "dek-1"
	tunnel := &fakeTunnel{unlock: func(string) string { return unlockReply(true, "") }}
	u := NewAgentDataUnlocker(nil, keys, "0.0.0.0:8443", time.Second)

	res, err := u.unlockOn(context.Background(), tunnel, &models.Qube{ID: "q1", Name: "remote-1"}, "dek-1", true, false)
	require.NoError(t, err)
	assert.True(t, res.Unlocked)
	assert.False(t, res.Migrated)
	assert.Zero(t, tunnel.rekeyCalls, "a working DEK must not trigger a rekey")
	assert.Zero(t, keys.legacyCalls, "the master must not be read for a migrated qube")
}

func TestUnlockOnMigratesLegacyDiskToMintedKey(t *testing.T) {
	keys := newFakeKeys()
	tunnel := &fakeTunnel{
		unlock: func(key string) string {
			require.Equal(t, "minted-dek-q1", key, "the disk must be opened with the new DEK after the rekey")
			return unlockReply(true, "")
		},
		rekey: func(payload string) string {
			var req struct{ Old, New string }
			require.NoError(t, json.Unmarshal([]byte(payload), &req))
			assert.Equal(t, "legacy-q1", req.Old)
			assert.Equal(t, "minted-dek-q1", req.New)
			return `{"rekeyed":true,"old_key_removed":true,"detail":"rekeyed"}`
		},
	}
	u := NewAgentDataUnlocker(nil, keys, "0.0.0.0:8443", time.Second)

	res, err := u.unlockOn(context.Background(), tunnel, &models.Qube{ID: "q1", Name: "remote-1"}, "", false, false)
	require.NoError(t, err)
	assert.True(t, res.Unlocked)
	assert.True(t, res.Migrated)
	assert.Equal(t, 1, keys.ensureCalls)
	assert.Equal(t, 1, keys.legacyCalls)
	assert.Equal(t, 1, tunnel.rekeyCalls)
	assert.Equal(t, 1, keys.markCalls, "the migration must be recorded before the rekey")
	assert.Equal(t, 1, keys.clearCalls, "a verified removal must clear the marker")
	assert.Equal(t, []string{"minted-dek-q1"}, tunnel.unlockKeys)
}

func TestUnlockOnRekeysToStoredKeyThatNoLongerOpens(t *testing.T) {
	keys := newFakeKeys()
	keys.stored["q1"] = "dek-1"
	tunnel := &fakeTunnel{}
	unlocks := 0
	tunnel.unlock = func(key string) string {
		unlocks++
		if key == "dek-1" && unlocks == 1 {
			return unlockReply(false, unlockReasonWrongKey)
		}
		return unlockReply(true, "")
	}
	tunnel.rekey = func(payload string) string {
		var req struct{ Old, New string }
		require.NoError(t, json.Unmarshal([]byte(payload), &req))
		assert.Equal(t, "legacy-q1", req.Old)
		assert.Equal(t, "dek-1", req.New, "the retry must reuse the stored key, not mint another")
		return `{"rekeyed":true,"old_key_removed":true,"detail":"rekeyed"}`
	}
	u := NewAgentDataUnlocker(nil, keys, "0.0.0.0:8443", time.Second)

	res, err := u.unlockOn(context.Background(), tunnel, &models.Qube{ID: "q1", Name: "remote-1"}, "dek-1", true, false)
	require.NoError(t, err)
	assert.True(t, res.Unlocked)
	assert.Equal(t, 1, tunnel.rekeyCalls)
	assert.Zero(t, keys.ensureCalls, "a stored key must not be replaced")
}

func TestUnlockOnDoesNotRekeyOnAgentRefusal(t *testing.T) {
	keys := newFakeKeys()
	keys.stored["q1"] = "dek-1"
	tunnel := &fakeTunnel{unlock: func(string) string { return unlockReply(false, "mount_failed") }}
	u := NewAgentDataUnlocker(nil, keys, "0.0.0.0:8443", time.Second)

	res, err := u.unlockOn(context.Background(), tunnel, &models.Qube{ID: "q1", Name: "remote-1"}, "dek-1", true, false)
	require.NoError(t, err)
	assert.False(t, res.Unlocked)
	assert.Equal(t, "mount_failed", res.Reason)
	assert.Zero(t, tunnel.rekeyCalls, "only a wrong-key result may trigger a rekey")
	assert.Zero(t, keys.legacyCalls)
}

func TestUnlockOnFinishesPendingRemovalAfterSuccessfulUnlock(t *testing.T) {
	keys := newFakeKeys()
	keys.stored["q1"] = "dek-1"
	tunnel := &fakeTunnel{
		unlock: func(string) string { return unlockReply(true, "") },
		rekey: func(string) string {
			return `{"rekeyed":true,"old_key_removed":true,"detail":"legacy keyslot removed"}`
		},
	}
	u := NewAgentDataUnlocker(nil, keys, "0.0.0.0:8443", time.Second)

	res, err := u.unlockOn(context.Background(), tunnel, &models.Qube{ID: "q1", Name: "remote-1"}, "dek-1", true, true)
	require.NoError(t, err)
	assert.True(t, res.Unlocked)
	assert.True(t, res.Migrated)
	assert.Equal(t, 1, tunnel.rekeyCalls, "a pending removal must be retried even when the DEK already opens the disk")
	assert.Equal(t, 1, keys.clearCalls)
}

func TestUnlockOnKeepsMarkerWhenRemovalFails(t *testing.T) {
	keys := newFakeKeys()
	keys.stored["q1"] = "dek-1"
	tunnel := &fakeTunnel{}
	unlocks := 0
	tunnel.unlock = func(string) string {
		unlocks++
		if unlocks == 1 {
			return unlockReply(false, unlockReasonWrongKey)
		}
		return unlockReply(true, "")
	}
	tunnel.rekey = func(string) string {
		return `{"rekeyed":true,"old_key_removed":false,"reason":"remove_failed","detail":"legacy slot survived"}`
	}
	u := NewAgentDataUnlocker(nil, keys, "0.0.0.0:8443", time.Second)

	res, err := u.unlockOn(context.Background(), tunnel, &models.Qube{ID: "q1", Name: "remote-1"}, "dek-1", true, false)
	require.NoError(t, err)
	assert.True(t, res.Unlocked)
	assert.True(t, res.Migrated)
	assert.Equal(t, 1, keys.markCalls)
	assert.Zero(t, keys.clearCalls, "an unremoved legacy slot must stay marked for retry")
	assert.True(t, keys.pending["q1"])
}

func TestUnlockOnFailsWithoutLegacyMaster(t *testing.T) {
	keys := newFakeKeys()
	keys.legacyErr = errors.New("master missing")
	tunnel := &fakeTunnel{unlock: func(string) string { return unlockReply(true, "") }}
	u := NewAgentDataUnlocker(nil, keys, "0.0.0.0:8443", time.Second)

	_, err := u.unlockOn(context.Background(), tunnel, &models.Qube{ID: "q1", Name: "remote-1"}, "", false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy data key")
	assert.Zero(t, tunnel.rekeyCalls, "nothing may be rekeyed without the key the disk was made with")
	assert.Zero(t, keys.ensureCalls, "no DEK may be minted before the migration can start")
}

func TestUnlockDataSkipsNonEncryptedQube(t *testing.T) {
	keys := newFakeKeys()
	u := NewAgentDataUnlocker(unreachedCA{t}, keys, "0.0.0.0:8443", time.Second)

	// A plaintext qube must not read a key or dial anything — the whole point
	// is that no key exists for a disk that was never meant to be encrypted.
	no := false
	u.UnlockData(context.Background(), &models.Qube{
		Name:      "remote-plain",
		IPAddress: "10.0.0.5",
		Spec:      models.QubeSpec{EncryptData: &no},
	})
	assert.Zero(t, keys.ensureCalls, "a non-encrypted qube must never touch the key store")

	// A nil qube is a no-op, not a panic.
	u.UnlockData(context.Background(), nil)
}

func TestUnlockRefusesWithoutAddress(t *testing.T) {
	keys := newFakeKeys()
	u := NewAgentDataUnlocker(unreachedCA{t}, keys, "0.0.0.0:8443", time.Second)

	// No address means nothing to dial; it must fail cleanly BEFORE reading the
	// key (no point handing a key to a host we cannot reach).
	yes := true
	_, err := u.Unlock(context.Background(), &models.Qube{
		Name: "remote-enc",
		Spec: models.QubeSpec{EncryptData: &yes},
	})
	require.Error(t, err)
	assert.Zero(t, keys.ensureCalls)
}

func TestUnlockNilReceiverErrs(t *testing.T) {
	var u *AgentDataUnlocker
	_, err := u.Unlock(context.Background(), &models.Qube{Name: "q", IPAddress: "1.2.3.4"})
	require.Error(t, err)
}
