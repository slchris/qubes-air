package grpc

import (
	"strings"
	"testing"
)

// TestBuildVersionDoesNotAffectCompatibility is the whole point of separating
// the two version fields. If a release bumped the wire version, every agent in
// the field would disconnect on upgrade day — so build version must never
// participate in a compatibility decision.
func TestBuildVersionDoesNotAffectCompatibility(t *testing.T) {
	original := BuildVersion
	defer func() { BuildVersion = original }()

	for _, build := range []string{"dev", "0.1.0", "99.99.99", ""} {
		BuildVersion = build
		if !supportsProtocol(protocolVersion) {
			t.Errorf("build %q must not affect protocol support", build)
		}
	}
}

// TestSupportsProtocol — the current version is served, and an unknown one is
// refused rather than optimistically accepted. Accepting an unknown version
// would let two peers proceed with different frame semantics, which fails later
// and far less legibly.
func TestSupportsProtocol(t *testing.T) {
	if !supportsProtocol(protocolVersion) {
		t.Errorf("this build must serve its own protocol version %q", protocolVersion)
	}
	for _, v := range []string{"", "v0", "v2", "V1", "1", "garbage"} {
		if supportsProtocol(v) {
			t.Errorf("must not claim to serve unknown protocol %q", v)
		}
	}
}

// TestProtocolMismatchMessageIsActionable — the message is what an operator
// reads when a connection is refused. It has to name both what arrived and what
// is served, or the reader cannot tell which side to upgrade.
func TestProtocolMismatchMessageIsActionable(t *testing.T) {
	msg := protocolMismatchMessage("v2")
	if !strings.Contains(msg, "v2") {
		t.Errorf("message must name the version received: %q", msg)
	}
	if !strings.Contains(msg, protocolVersion) {
		t.Errorf("message must name the version served: %q", msg)
	}
	if !strings.Contains(msg, "upgrade") {
		t.Errorf("message must say what to do about it: %q", msg)
	}

	// An agent that sends nothing at all is a distinct case worth its own
	// wording — "peer speaks """ reads like a bug in our own code.
	empty := protocolMismatchMessage("")
	if !strings.Contains(empty, "no protocol version") {
		t.Errorf("an absent version needs its own wording: %q", empty)
	}
}

// TestErrorCodesAreStable pins the CallError codes. They are matched by callers
// and shown to operators, so renaming one silently breaks whoever switches on
// it — this test makes that a deliberate act.
func TestErrorCodesAreStable(t *testing.T) {
	want := map[string]string{
		"protocol mismatch": CodeProtocolMismatch,
		"denied":            CodeDenied,
		"timeout":           CodeTimeout,
		"unavailable":       CodeUnavailable,
		"internal":          CodeInternal,
	}
	expect := map[string]string{
		"protocol mismatch": "PROTOCOL_MISMATCH",
		"denied":            "DENIED",
		"timeout":           "TIMEOUT",
		"unavailable":       "UNAVAILABLE",
		"internal":          "INTERNAL",
	}
	for k, got := range want {
		if got != expect[k] {
			t.Errorf("%s code changed: %q (was %q) — this breaks callers matching on it", k, got, expect[k])
		}
	}
}

// TestHandshakeCarriesBuildVersion — an operator debugging a remote needs to
// know which agent build is actually running, and the handshake is the only
// place that information crosses.
func TestHandshakeCarriesBuildVersion(t *testing.T) {
	original := BuildVersion
	defer func() { BuildVersion = original }()
	BuildVersion = "1.2.3"

	f := handshakeFrame("sys-relay-pve", "remote-dev")
	hs := f.GetHandshake()
	if hs == nil {
		t.Fatal("expected a Handshake frame")
	}
	if hs.GetBuildVersion() != "1.2.3" {
		t.Errorf("build version must cross the wire, got %q", hs.GetBuildVersion())
	}
	if hs.GetProtocolVersion() != protocolVersion {
		t.Errorf("protocol version must be the constant, got %q", hs.GetProtocolVersion())
	}
}
