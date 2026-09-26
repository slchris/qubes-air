package models

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestMatchesConsoleName(t *testing.T) {
	const want = "qubes-air-ca-key"
	for _, stored := range []string{
		want, "QUBES-AIR-CA-KEY", "qube\u017f-air-ca-key", "qubes-air-ca-\u212aey", " qubes-air-ca-key\t",
	} {
		assert.True(t, MatchesConsoleName(stored, want), "%q", stored)
		assert.True(t, IsConsoleCredential(stored, "other"), "%q answers to a console name, so it must be a console row", stored)
	}
	for _, stored := range []string{
		"qubes-air-ca-key2", "qubes-air-ca-ke", "qubes-a\u0130r-ca-key", "my-qubes-air-ca-key", "",
	} {
		assert.False(t, MatchesConsoleName(stored, want), "%q", stored)
	}
}

func TestSelectConsoleRow(t *testing.T) {
	const name = "qubes-air-ca-key"
	genuine := Credential{ID: "id-genuine", Name: name, Type: ConsoleRowType}
	operator := Credential{ID: "id-operator", Name: "pve-prod", Type: "proxmox"}

	got, err := SelectConsoleRow([]Credential{operator, genuine}, name)
	require.NoError(t, err)
	assert.Equal(t, genuine, got)

	_, err = SelectConsoleRow([]Credential{operator}, name)
	assert.ErrorIs(t, err, ErrConsoleRowNotFound)
	assert.NotErrorIs(t, err, ErrConsoleRowConflict)

	for label, rows := range map[string][]Credential{
		"duplicate":                 {genuine, {ID: "id-dup", Name: name, Type: ConsoleRowType}},
		"case variant":              {{ID: "id-case", Name: "QUBES-AIR-CA-KEY", Type: ConsoleRowType}},
		"long s variant":            {{ID: "id-long-s", Name: "qube\u017f-air-ca-key", Type: ConsoleRowType}},
		"Kelvin variant":            {{ID: "id-kelvin", Name: "qubes-air-ca-\u212aey", Type: ConsoleRowType}},
		"padded":                    {{ID: "id-pad", Name: name + " ", Type: ConsoleRowType}},
		"wrong type":                {{ID: "id-type", Name: name, Type: "other"}},
		"type differs only by case": {{ID: "id-pki", Name: name, Type: "PKI"}},
	} {
		t.Run(label, func(t *testing.T) {
			_, err := SelectConsoleRow(append([]Credential{operator}, rows...), name)
			require.ErrorIs(t, err, ErrConsoleRowConflict)
			assert.NotErrorIs(t, err, ErrConsoleRowNotFound)
			for _, row := range rows {
				assert.Contains(t, err.Error(), row.ID)
			}
			assert.NotContains(t, err.Error(), operator.ID, "only rows answering to the name are named")
		})
	}
}

// TestSelectConsoleRowBoundsTheError — a store stuffed with look-alikes must
// not turn one refusal into an unbounded log line, and a control character in
// a planted name must not forge a second line.
func TestSelectConsoleRowBoundsTheError(t *testing.T) {
	const name = "qubes-air-ca-key"
	rows := make([]Credential, 0, 100)
	for i := range 100 {
		rows = append(rows, Credential{ID: fmt.Sprintf("id-%03d", i), Name: "QUBES-AIR-CA-KEY", Type: ConsoleRowType})
	}
	// Trailing white space still answers to the name, and is quoted when named.
	rows[0].Name = name + "\n"
	_, err := SelectConsoleRow(rows, name)
	require.ErrorIs(t, err, ErrConsoleRowConflict)
	assert.Contains(t, err.Error(), "100 row(s)")
	assert.Contains(t, err.Error(), "and 92 more")
	assert.NotContains(t, err.Error(), "id-050", "only the first rows are named")
	assert.NotContains(t, err.Error(), "\n", "names are quoted, so no raw newline reaches a log line")
}

// TestSelectConsoleRowBoundsLongFields — planted names and types are chosen by
// whoever planted them. A megabyte-long look-alike must not become a
// megabyte-long error: each quoted field is cut, the cut is marked with the
// bytes left out, and the row ID stays visible.
func TestSelectConsoleRowBoundsLongFields(t *testing.T) {
	const name = "qubes-air-ca-key"
	pad := strings.Repeat(" ", 1<<20)
	rows := make([]Credential, 0, 22)
	rows = append(rows,
		Credential{ID: "id-padded", Name: pad + name, Type: ConsoleRowType},
		Credential{ID: "id-long-type", Name: name, Type: strings.Repeat("\x00", 1<<20)},
	)
	for i := range 20 {
		rows = append(rows, Credential{ID: fmt.Sprintf("id-%02d", i), Name: name + pad, Type: pad})
	}

	_, err := SelectConsoleRow(rows, name)
	require.ErrorIs(t, err, ErrConsoleRowConflict)
	msg := err.Error()
	assert.LessOrEqual(t, len(msg), 8<<10, "a conflict error must stay under 8 KiB")
	assert.Contains(t, msg, "id-padded")
	assert.Contains(t, msg, "id-long-type")
	assert.Contains(t, msg, fmt.Sprintf("…(+%d bytes)", len(pad)+len(name)-maxQuotedField))
	assert.Contains(t, msg, "and 14 more")
	assert.True(t, utf8.ValidString(msg))
	assert.NotContains(t, msg, "\x00", "control characters stay escaped")
}

func TestQuoteCapped(t *testing.T) {
	assert.Equal(t, `"qubes-air-ca-key"`, quoteCapped("qubes-air-ca-key"))
	exact := strings.Repeat("a", maxQuotedField)
	assert.Equal(t, strconv.Quote(exact), quoteCapped(exact), "a field of exactly the cap is not cut")

	// "€" is three bytes, so the cut must step back to a rune boundary.
	euros := strings.Repeat("€", 100)
	got := quoteCapped(euros)
	assert.True(t, utf8.ValidString(got))
	assert.True(t, strings.HasPrefix(got, `"`+strings.Repeat("€", maxQuotedField/3)+`"…(+`), got)
	assert.True(t, strings.HasSuffix(got, fmt.Sprintf("(+%d bytes)", len(euros)-maxQuotedField/3*3)), got)
}
