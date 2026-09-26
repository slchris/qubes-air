package qrexec

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/slchris/qubes-air/console/internal/transport"
)

// The two services whose power is bounded by a path allowlist.
const (
	ExecService     = "qubesair.Exec"
	FileCopyService = "qubesair.FileCopy"
)

// GrantLabels names the three grant lists in messages, so each caller reports
// them under the names its operator actually configured.
type GrantLabels struct {
	Services string
	Exec     string
	FileCopy string
}

// AgentEnvLabels are the names the lists carry in the guest's agent.env.
var AgentEnvLabels = GrantLabels{
	Services: "QUBESAIR_ALLOW",
	Exec:     "QUBESAIR_EXEC_ALLOW",
	FileCopy: "QUBESAIR_FILECOPY_ROOTS",
}

// ValidateAgentGrants checks the whole privilege set delivered to one agent:
// the qrexec services it may run (comma-separated in agent.env) and the two
// path allowlists that bound Exec and FileCopy (colon-separated).
//
// It returns an error for anything that cannot be delivered as configured: a
// service name the transport would refuse or that could smuggle a second
// agent.env line or list entry (commas, whitespace, control characters), a
// duplicate service or path, and every path rule ValidatePathAllowlist
// enforces.
//
// Separately it returns warnings for grants that are deliverable but inert: a
// path allowlist whose service is not in the service list. The agent refuses
// that service outright, so the paths grant nothing; that is a configuration
// that does not do what its author meant, not a hole, and it is reported
// rather than refused so a console configured that way still starts.
func ValidateAgentGrants(labels GrantLabels, services, execAllow, fileCopyRoots []string) ([]string, error) {
	if err := validateServiceGrants(labels.Services, services); err != nil {
		return nil, err
	}
	if err := ValidatePathAllowlist(labels.Exec, execAllow, false); err != nil {
		return nil, err
	}
	if err := ValidatePathAllowlist(labels.FileCopy, fileCopyRoots, true); err != nil {
		return nil, err
	}
	var warnings []string
	for _, pair := range []struct {
		label, service string
		paths          []string
	}{
		{labels.Exec, ExecService, execAllow},
		{labels.FileCopy, FileCopyService, fileCopyRoots},
	} {
		if len(pair.paths) > 0 && !slices.Contains(services, pair.service) {
			warnings = append(warnings, fmt.Sprintf(
				"%s lists %d path(s) but %s does not allow %s, so the agent will refuse every %s call; "+
					"add %s to %s or clear %s",
				pair.label, len(pair.paths), labels.Services, pair.service, pair.service,
				pair.service, labels.Services, pair.label))
		}
	}
	return warnings, nil
}

// validateServiceGrants refuses a service name the agent could not be granted
// as written. transport.ValidName is the same allowlist every name on the wire
// passes, and it already excludes ',', whitespace and control characters —
// the characters that would split one entry into two or start a new
// agent.env line.
func validateServiceGrants(field string, services []string) error {
	seen := make(map[string]struct{}, len(services))
	for _, service := range services {
		if !transport.ValidName(service) {
			return fmt.Errorf("%s: %q is not a valid qrexec service name", field, service)
		}
		if _, dup := seen[service]; dup {
			return fmt.Errorf("%s: %q is listed twice", field, service)
		}
		seen[service] = struct{}{}
	}
	return nil
}

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
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, dup := seen[entry]; dup {
			return fmt.Errorf("%s: %q is listed twice", field, entry)
		}
		seen[entry] = struct{}{}
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
		case strings.ContainsFunc(entry, isControl):
			// Any C0 control or DEL, not just the line breaks: a tab or an escape
			// sequence in a path delivered to a root service is never intended.
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

// isControl reports a C0 control character or DEL.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }
