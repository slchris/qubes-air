package models

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The credentials table holds two kinds of row.
//
// Operator rows are the provider secrets a zone references, and the
// credentials API exists to manage them. Console rows are the console's own
// secrets, kept in the same table so one keyring protects them and one tool
// rotates them: the agent CA certificate and private key, the legacy LUKS
// master, one data key (DEK) per qube and one migration marker per qube whose
// disk may still open with the legacy key. Whoever holds the CA key can mint
// any agent identity, and deleting a DEK is the crypto-shred that makes purge
// irreversible, so the credentials API must neither expose nor change these
// rows, and must not let an operator row take a name the console looks its own
// secrets up by.
//
// The rule is defined here and nowhere else. A row belongs to the console when
// its type is ConsoleRowType or its name is in the ConsoleRowNamePrefix
// namespace. Every row the console writes carries both. The type catches a
// future console row whose name was left out of the namespace; the name
// catches an operator row that would shadow a console secret, because the
// console finds its secrets by name alone.
const (
	// ConsoleRowType is the type of every credential row the console writes
	// for itself.
	ConsoleRowType = "pki"
	// ConsoleRowNamePrefix is the namespace of console credential row names,
	// such as qubes-air-ca-key and qubes-air-luks-key-<qube id>. (The constant
	// names avoid the word "credential": gosec G101 reads any such constant
	// holding a string as a hard-coded secret.)
	ConsoleRowNamePrefix = "qubes-air-"
)

// IsConsoleCredential reports whether a credential row with this name and type
// belongs to the console rather than to the operator.
//
// Every name MatchesConsoleName accepts for a console name is classified here:
// both ignore surrounding white space and compare under Unicode simple case
// folding, this one rune by rune over the prefix. So "QUBES-AIR-CA-KEY", or a
// spelling that uses a fold-equivalent rune such as U+017F (long s, which
// folds to "s") or U+212A (Kelvin sign, which folds to "k"), is as reserved as
// the lower-case form.
func IsConsoleCredential(name, typ string) bool {
	return strings.EqualFold(strings.TrimSpace(typ), ConsoleRowType) ||
		hasFoldPrefix(strings.TrimSpace(name), ConsoleRowNamePrefix)
}

// hasFoldPrefix reports whether s begins with prefix under Unicode simple case
// folding. Simple folding maps one rune to one rune, so comparing the leading
// runes is exactly what strings.EqualFold would decide for the whole name.
func hasFoldPrefix(s, prefix string) bool {
	for _, want := range prefix {
		got, size := utf8.DecodeRuneInString(s)
		if size == 0 || !strings.EqualFold(string(got), string(want)) {
			return false
		}
		s = s[size:]
	}
	return true
}

// Errors from SelectConsoleRow.
var (
	// ErrConsoleRowNotFound means no row answers to the console name.
	ErrConsoleRowNotFound = errors.New("console credential not found")
	// ErrConsoleRowConflict means rows answer to the console name that the
	// console did not write. It never comes with secret material.
	ErrConsoleRowConflict = errors.New("console credential is ambiguous")
)

// maxConflictRowsNamed bounds how many conflicting rows an error names, so a
// table stuffed with look-alike rows cannot turn one lookup into a huge log
// line.
const maxConflictRowsNamed = 8

// MatchesConsoleName reports whether a stored row name answers to the console
// name want. It is the one comparison every console lookup and deletion uses:
// surrounding white space is ignored and letters are compared under Unicode
// simple case folding (strings.EqualFold).
func MatchesConsoleName(stored, want string) bool {
	return strings.EqualFold(strings.TrimSpace(stored), want)
}

// SelectConsoleRow returns the one row the console stored under name.
//
// The console writes exactly one row per name, spelled exactly as name and
// typed ConsoleRowType. Any other row that answers to the name was written by
// someone else, which the credentials API allowed until it reserved the
// namespace. Taking the newest match, as the lookups used to, let such a row
// stand in for the CA or a data key. So this fails closed instead of guessing
// which row is genuine:
//
//   - no row answers to name: ErrConsoleRowNotFound;
//   - more than one row answers, or the one that does is not spelled exactly
//     as name or not typed exactly ConsoleRowType: ErrConsoleRowConflict,
//     naming the rows by ID so an operator can remove the ones the console
//     did not write.
func SelectConsoleRow(rows []Credential, name string) (Credential, error) {
	var matches []Credential
	for _, row := range rows {
		if MatchesConsoleName(row.Name, name) {
			matches = append(matches, row)
		}
	}
	switch {
	case len(matches) == 0:
		return Credential{}, fmt.Errorf("%w: %q", ErrConsoleRowNotFound, name)
	case len(matches) == 1 && matches[0].Name == name && matches[0].Type == ConsoleRowType:
		return matches[0], nil
	}
	return Credential{}, fmt.Errorf("%w: %d row(s) answer to %q [%s]; the console writes exactly one, "+
		"named %q with type %q, so it uses none of them. Stop the console, back up the database and "+
		"delete the rows it did not write (see docs/security-controls.md)",
		ErrConsoleRowConflict, len(matches), name, describeRows(matches), name, ConsoleRowType)
}

// describeRows names rows by ID, name and type — never by secret. Names and
// types are quoted, so a control character in one cannot forge a log line.
func describeRows(rows []Credential) string {
	parts := make([]string, 0, min(len(rows), maxConflictRowsNamed)+1)
	for i, row := range rows {
		if i == maxConflictRowsNamed {
			parts = append(parts, fmt.Sprintf("and %d more", len(rows)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("id=%s name=%q type=%q", row.ID, row.Name, row.Type))
	}
	return strings.Join(parts, "; ")
}
