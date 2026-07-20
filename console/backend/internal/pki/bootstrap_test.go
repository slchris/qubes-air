package pki

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTokenVerifiesForItsOwnQube(t *testing.T) {
	secret, rec, err := NewBootstrapToken("remote-dev-1", time.Hour)
	if err != nil {
		t.Fatalf("NewBootstrapToken: %v", err)
	}
	if err := rec.Verify(secret, "remote-dev-1", time.Now()); err != nil {
		t.Fatalf("a freshly minted token was refused: %v", err)
	}
}

// The record is what gets stored, so it must not contain anything that can be
// replayed. This is the same reason a password store keeps digests.
func TestRecordDoesNotContainTheSecret(t *testing.T) {
	secret, rec, err := NewBootstrapToken("remote-dev-1", time.Hour)
	if err != nil {
		t.Fatalf("NewBootstrapToken: %v", err)
	}
	if strings.Contains(rec.SecretHash, secret) || rec.SecretHash == secret {
		t.Fatal("the stored record contains the token itself")
	}
	if len(secret) < 40 {
		t.Errorf("token looks too short to resist guessing: %d chars", len(secret))
	}
}

// Single use is the property that makes a leaked token worthless after the
// fact — without it this is just a long-lived shared password.
func TestTokenCannotBeRedeemedTwice(t *testing.T) {
	secret, rec, _ := NewBootstrapToken("remote-dev-1", time.Hour)
	now := time.Now()

	if err := rec.Verify(secret, "remote-dev-1", now); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if err := rec.Redeem(now); err != nil {
		t.Fatalf("first redeem: %v", err)
	}

	if err := rec.Redeem(now); !errors.Is(err, ErrTokenRedeemed) {
		t.Errorf("second redeem returned %v, want ErrTokenRedeemed", err)
	}
	if err := rec.Verify(secret, "remote-dev-1", now); !errors.Is(err, ErrTokenRedeemed) {
		t.Errorf("verify after redemption returned %v, want ErrTokenRedeemed", err)
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	secret, rec, _ := NewBootstrapToken("remote-dev-1", time.Minute)
	later := time.Now().Add(2 * time.Minute)

	if err := rec.Verify(secret, "remote-dev-1", later); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("got %v, want ErrTokenExpired", err)
	}
}

// A token authorizes ONE name. Without this a token leaked from any qube would
// mint a certificate for the most valuable one.
func TestTokenIsBoundToItsQube(t *testing.T) {
	secret, rec, _ := NewBootstrapToken("remote-dev-1", time.Hour)

	if err := rec.Verify(secret, "remote-prod-1", time.Now()); !errors.Is(err, ErrTokenMismatch) {
		t.Errorf("a token minted for remote-dev-1 was accepted for remote-prod-1: %v", err)
	}
}

func TestWrongSecretIsRefused(t *testing.T) {
	_, rec, _ := NewBootstrapToken("remote-dev-1", time.Hour)

	if err := rec.Verify("not-the-token", "remote-dev-1", time.Now()); !errors.Is(err, ErrTokenMismatch) {
		t.Errorf("got %v, want ErrTokenMismatch", err)
	}
}

// A wrong secret and a wrong name return the SAME error on purpose: saying
// which half was wrong tells an attacker which half to keep guessing.
func TestWrongSecretAndWrongNameAreIndistinguishable(t *testing.T) {
	secret, rec, _ := NewBootstrapToken("remote-dev-1", time.Hour)
	now := time.Now()

	badSecret := rec.Verify("wrong", "remote-dev-1", now)
	badName := rec.Verify(secret, "wrong", now)

	if badSecret == nil || badName == nil {
		t.Fatal("expected both to fail")
	}
	if badSecret.Error() != badName.Error() {
		t.Errorf("errors differ and leak which half was wrong:\n  secret: %v\n  name:   %v",
			badSecret, badName)
	}
}

func TestTokensAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		s, _, err := NewBootstrapToken("remote-dev-1", time.Hour)
		if err != nil {
			t.Fatalf("NewBootstrapToken: %v", err)
		}
		if seen[s] {
			t.Fatal("the generator repeated a token")
		}
		seen[s] = true
	}
}

func TestRejectsUnusableArguments(t *testing.T) {
	if _, _, err := NewBootstrapToken("", time.Hour); err == nil {
		t.Error("accepted an empty qube name")
	}
	// A non-positive lifetime would mint a token that is expired on arrival,
	// which reads to the operator as a broken bootstrap rather than a bad call.
	if _, _, err := NewBootstrapToken("remote-dev-1", 0); err == nil {
		t.Error("accepted a zero lifetime")
	}
}

// A nil record must refuse rather than panic: it is what a lookup for an
// unknown qube returns, which is exactly the path an attacker probes.
func TestNilRecordRefuses(t *testing.T) {
	var rec *BootstrapRecord
	if err := rec.Verify("anything", "remote-dev-1", time.Now()); err == nil {
		t.Error("a nil record accepted a token")
	}
	if err := rec.Redeem(time.Now()); err == nil {
		t.Error("a nil record was redeemable")
	}
	if rec.Redeemed() {
		t.Error("a nil record reported itself redeemed")
	}
}
