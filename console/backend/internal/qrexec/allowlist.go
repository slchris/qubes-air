package qrexec

import (
	"fmt"
	"path"
	"strings"
)

// ValidatePathAllowlist checks one agent path allowlist before it is delivered
// to a guest, where the agent re-checks it before acting.
//
// Two services take an allowlist shaped like this, both colon-separated in
// agent.env and both reading it as a hard requirement rather than a hint:
//
//	QUBESAIR_EXEC_ALLOW     absolute program paths, e.g. /usr/bin/id
//	QUBESAIR_FILECOPY_ROOTS absolute directories, e.g. /var/tmp
//
// The agent rejects an empty allowlist outright ("service disabled", exit 77),
// so an unset value disables the service rather than allowing everything. These
// rules mirror what remote/qubes-rpc/qubesair.Exec and .FileCopy enforce at use
// time, and they are checked here so a typo fails at startup or at provision
// time instead of surfacing as a refused call in a guest nobody is watching.
//
// forbidRoot is set for FileCopy roots: "/" as a root would allow every path on
// the host, and the agent refuses it too.
func ValidatePathAllowlist(field string, entries []string, forbidRoot bool) error {
	for _, entry := range entries {
		switch {
		case entry == "":
			return fmt.Errorf("%s: empty entry", field)
		case !strings.HasPrefix(entry, "/"):
			return fmt.Errorf("%s: %q is not an absolute path", field, entry)
		case strings.ContainsRune(entry, ':'):
			// ':' separates entries on the wire, so an entry containing one
			// cannot be delivered: the agent would split it into two paths that
			// were never configured.
			return fmt.Errorf("%s: %q contains ':', which separates entries", field, entry)
		case strings.ContainsAny(entry, "\n\r\x00"):
			return fmt.Errorf("%s: %q contains a control character", field, entry)
		case path.Clean(entry) != entry:
			// Also rejects "..", "." and duplicate slashes: the agent compares
			// against the request path literally, so an unnormalized entry
			// silently matches nothing.
			return fmt.Errorf("%s: %q is not normalized (want %q)", field, entry, path.Clean(entry))
		case forbidRoot && entry == "/":
			return fmt.Errorf("%s: \"/\" would allow the whole filesystem", field)
		}
	}
	return nil
}

// JoinPathAllowlist renders an allowlist for agent.env.
//
// A single entry is joined to itself, so an empty list stays empty and the
// caller can omit the key entirely: the agent's "empty means disabled" rule is
// the safe default, and writing "KEY=" would be the same thing spelled less
// clearly.
func JoinPathAllowlist(entries []string) string {
	return strings.Join(entries, ":")
}
