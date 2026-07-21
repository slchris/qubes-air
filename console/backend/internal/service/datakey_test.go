package service

import (
	"context"
	"testing"
)

func TestDataKeyManagerMintsMasterOnceAndDerivesStably(t *testing.T) {
	store := newMemCredStore()
	ctx := context.Background()

	m := NewDataKeyManager(store)

	// First use mints and stores the master; the same qube must derive the same
	// key on every call.
	k1, err := m.DataKeyFor(ctx, "qube-A")
	if err != nil {
		t.Fatalf("DataKeyFor A: %v", err)
	}
	k1again, err := m.DataKeyFor(ctx, "qube-A")
	if err != nil {
		t.Fatalf("DataKeyFor A again: %v", err)
	}
	if k1 == "" || k1 != k1again {
		t.Fatalf("same qube derived different keys: %q vs %q", k1, k1again)
	}

	// A different qube must get a different key.
	k2, err := m.DataKeyFor(ctx, "qube-B")
	if err != nil {
		t.Fatalf("DataKeyFor B: %v", err)
	}
	if k1 == k2 {
		t.Fatal("two qubes share a data key")
	}

	// The master must be persisted, not re-minted: a fresh manager over the SAME
	// store must derive the SAME key — otherwise a console restart would lock
	// every encrypted disk away.
	m2 := NewDataKeyManager(store)
	k1fresh, err := m2.DataKeyFor(ctx, "qube-A")
	if err != nil {
		t.Fatalf("fresh manager DataKeyFor A: %v", err)
	}
	if k1fresh != k1 {
		t.Fatalf("master was re-minted across managers: %q != %q", k1fresh, k1)
	}

	// Exactly one master credential should exist in the store.
	list, _ := store.List(ctx)
	n := 0
	for _, c := range list {
		if c.Name == dataMasterCredentialName {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one master credential, found %d", n)
	}
}
