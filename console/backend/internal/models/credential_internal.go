package models

import (
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
// It matches at least as loosely as the console's own lookups, which compare
// names with strings.EqualFold. The prefix is compared rune by rune under
// Unicode simple case folding, so "QUBES-AIR-CA-KEY", or a spelling that uses
// a fold-equivalent rune such as U+017F (long s, which folds to "s"), is as
// reserved as the lower-case form. Surrounding white space is ignored for the
// same reason: a near-copy of a reserved name must not get through.
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
