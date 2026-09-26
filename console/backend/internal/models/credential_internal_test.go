package models

import (
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
)

func TestIsConsoleCredential(t *testing.T) {
	tests := []struct {
		name, credName, credType string
		want                     bool
	}{
		// Every row the console writes, with the type it writes.
		{"CA certificate", "qubes-air-ca-cert", "pki", true},
		{"CA private key", "qubes-air-ca-key", "pki", true},
		{"legacy LUKS master", "qubes-air-luks-master", "pki", true},
		{"per-qube data key", "qubes-air-luks-key-6b1f0c2e", "pki", true},
		{"migration marker", "qubes-air-luks-legacy-slot-6b1f0c2e", "pki", true},

		// The name alone reserves a row: the console looks its secrets up by
		// name, whatever type the row carries.
		{"reserved name, operator type", "qubes-air-ca-key", "proxmox", true},
		{"reserved name, no type", "qubes-air-luks-key-q1", "", true},
		{"bare prefix", "qubes-air-", "other", true},
		{"upper case", "QUBES-AIR-CA-KEY", "other", true},
		{"mixed case", "Qubes-Air-Luks-Key-q1", "api_key", true},
		// U+017F LATIN SMALL LETTER LONG S folds to "s", so strings.EqualFold
		// (what the console's lookups use) treats this as qubes-air-ca-key.
		{"fold-equivalent rune", "qubeſ-air-ca-key", "other", true},
		{"leading space", " qubes-air-ca-key", "other", true},
		{"trailing tab", "qubes-air-ca-key\t", "other", true},

		// The type alone reserves a row, whatever its name.
		{"console type", "anything", "pki", true},
		{"console type upper case", "anything", "PKI", true},
		{"console type padded", "anything", " pki ", true},

		// Operator rows.
		{"proxmox token", "pve-prod", "proxmox", false},
		{"prefix not at start", "my-qubes-air-token", "api_key", false},
		{"prefix without dash", "qubes-air", "other", false},
		{"similar but different", "qubes-airport", "other", false},
		{"underscore instead of dash", "qubes_air-ca-key", "other", false},
		{"type that only contains pki", "legacy", "pki-archive", false},
		{"empty", "", "", false},
		{"invalid UTF-8", "qubes-\xffir-ca-key", "other", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsConsoleCredential(tt.credName, tt.credType))
		})
	}
}

// TestIsConsoleCredentialCoversEveryFoldOfTheLookupNames — the console finds
// its secrets with strings.EqualFold, so any name that lookup would accept as
// a console name must be reserved. Every rune of the prefix is replaced in
// turn by each rune in its case-fold orbit.
func TestIsConsoleCredentialCoversEveryFoldOfTheLookupNames(t *testing.T) {
	const lookup = "qubes-air-ca-key"
	for i, r := range lookup {
		for _, alt := range foldOrbit(r) {
			variant := lookup[:i] + string(alt) + lookup[i+1:]
			if !strings.EqualFold(variant, lookup) {
				t.Fatalf("test bug: %q is not a fold of %q", variant, lookup)
			}
			assert.True(t, IsConsoleCredential(variant, "other"), "%q folds to %q", variant, lookup)
		}
	}
}

// foldOrbit returns r and every rune Unicode simple case folding maps it to,
// which is what strings.EqualFold treats as equal to r.
func foldOrbit(r rune) []rune {
	orbit := []rune{r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		orbit = append(orbit, f)
	}
	return orbit
}
