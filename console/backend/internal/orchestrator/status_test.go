package orchestrator

import (
	"strings"
	"testing"
)

// sampleOutput is the shape `terraform output -json remote_qubes` produces.
const sampleOutput = `{
  "dev-work": {"status": "running", "ip_address": "10.31.0.51", "compute_running": true, "data_disk_id": "d-1"},
  "parked":   {"status": "suspended", "ip_address": "", "compute_running": false, "data_disk_id": "d-2"}
}`

// TestParseQubeAddress — terraform has always emitted ip_address and nothing
// read it back, so qubes.ip_address stayed empty for every qube ever created.
// That is what made agent health unanswerable: the agent listens on the qube's
// OWN address, so with none recorded there was nothing to dial and a running
// agent looked exactly like a dead one.
func TestParseQubeAddress(t *testing.T) {
	got, err := parseQubeAddress(sampleOutput, "dev-work")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != "10.31.0.51" {
		t.Errorf("address: got %q, want %q", got, "10.31.0.51")
	}

	// A suspended qube has no compute instance and so no address. Empty is the
	// correct answer, not an error: the caller then reports "no address", which
	// is its own honest diagnosis.
	got, err = parseQubeAddress(sampleOutput, "parked")
	if err != nil {
		t.Fatalf("parse suspended: %v", err)
	}
	if got != "" {
		t.Errorf("a suspended qube should have no address, got %q", got)
	}
}

// TestParseQubeAddressUnknownQube — asking about a qube terraform has never
// heard of must fail loudly rather than return an empty address, which would be
// indistinguishable from a qube that is merely suspended.
func TestParseQubeAddressUnknownQube(t *testing.T) {
	_, err := parseQubeAddress(sampleOutput, "ghost")
	if err == nil {
		t.Fatal("a qube absent from terraform output must be an error, not an empty address")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("the error must name the qube, got %v", err)
	}
}

// TestParseQubeStatusAndAddressShareTheDecode guards the refactor: both readers
// go through parseQubeOutput, so a malformed payload must fail the same way for
// each rather than one of them silently returning a zero value.
func TestParseQubeStatusAndAddressShareTheDecode(t *testing.T) {
	const garbage = `not json at all`

	if _, err := parseQubeStatus(garbage, "dev-work"); err == nil {
		t.Error("status: malformed terraform output must be an error")
	}
	if _, err := parseQubeAddress(garbage, "dev-work"); err == nil {
		t.Error("address: malformed terraform output must be an error")
	}

	status, err := parseQubeStatus(sampleOutput, "dev-work")
	if err != nil || status != "running" {
		t.Errorf("status: got %q, %v", status, err)
	}
}
