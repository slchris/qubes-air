package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingKeys reports whether a key was ever derived.
type recordingKeys struct{ called bool }

func (r *recordingKeys) DataKeyFor(context.Context, string) (string, error) {
	r.called = true
	return "derived-key", nil
}

// unreachedCA fails if consulted; the tests that use it must never reach a dial.
type unreachedCA struct{ t *testing.T }

func (c unreachedCA) CA(context.Context) (*pki.CA, error) {
	c.t.Helper()
	c.t.Fatal("CA must not be consulted on this path")
	return nil, errors.New("unreachable")
}

func TestUnlockDataSkipsNonEncryptedQube(t *testing.T) {
	keys := &recordingKeys{}
	u := NewAgentDataUnlocker(unreachedCA{t}, keys, "0.0.0.0:8443", time.Second)

	// A plaintext qube must not derive a key or dial anything — the whole point
	// is that no key exists for a disk that was never meant to be encrypted.
	no := false
	u.UnlockData(context.Background(), &models.Qube{
		Name:      "remote-plain",
		IPAddress: "10.0.0.5",
		Spec:      models.QubeSpec{EncryptData: &no},
	})
	assert.False(t, keys.called, "a non-encrypted qube must never derive a data key")

	// A nil qube is a no-op, not a panic.
	u.UnlockData(context.Background(), nil)
}

func TestUnlockRefusesWithoutAddress(t *testing.T) {
	keys := &recordingKeys{}
	u := NewAgentDataUnlocker(unreachedCA{t}, keys, "0.0.0.0:8443", time.Second)

	// No address means nothing to dial; it must fail cleanly BEFORE deriving the
	// key (no point handing a key to a host we cannot reach).
	yes := true
	_, err := u.Unlock(context.Background(), &models.Qube{
		Name: "remote-enc",
		Spec: models.QubeSpec{EncryptData: &yes},
	})
	require.Error(t, err)
	assert.False(t, keys.called, "must not derive a key when there is no address to dial")
}

func TestUnlockNilReceiverErrs(t *testing.T) {
	var u *AgentDataUnlocker
	_, err := u.Unlock(context.Background(), &models.Qube{Name: "q", IPAddress: "1.2.3.4"})
	require.Error(t, err)
}
