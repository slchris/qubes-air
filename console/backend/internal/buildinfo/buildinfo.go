// Package buildinfo reports the build metadata the linker stamped into the
// console binary.
//
// Why this exists: an operator has to be able to tell which build a running
// console is, both right after an upgrade and right after a rollback that has to
// be confirmed from the service itself. A compile-time constant cannot answer
// that — it is the same string in every build ever made, which is exactly what
// `"0.1.0"` was (gap G-H8), and what makes it worse is that it looks like an
// answer.
//
// The three values come from the linker, never from the source:
//
//	go build -ldflags "\
//	  -X <pkg>.version=$(git describe --tags --always --dirty) \
//	  -X <pkg>.revision=$(git rev-parse HEAD) \
//	  -X <pkg>.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// <pkg> is this package's import path. The Makefile (build-backend) and
// .github/workflows/release.yml pass exactly those flags; a build that passes
// none of them reports Unstamped for every field rather than a plausible
// version. -X matching nothing is silent, so cmd/server/version_test.go builds
// the real binary with the real flags and runs it — a rename here fails there.
package buildinfo

import (
	"fmt"
	"strings"
)

// Unstamped is what every field reports for a binary built without the -X
// stamps: a plain `go build ./cmd/server`, or a build whose flags stopped
// matching a renamed variable (-X fails silently).
//
// It is deliberately not version-shaped. "0.1.0" read as an answer to "which
// build is this?" and "dev" reads as a stage of one; neither is an answer, and
// both cost the operator the time it takes to find that out. `unknown` says
// what is true — nobody stamped this binary — so the next step is to go look at
// how it was built.
const Unstamped = "unknown"

// Stamped at link time. Kept unexported so the only way to read them is Get,
// which is where the empty-means-unknown rule lives; a caller cannot
// accidentally read a raw field that the linker never filled in.
var (
	version   = ""
	revision  = ""
	buildTime = ""
)

// dirtySuffix is what `git describe --dirty` appends to a version whose working
// tree had uncommitted changes. Recognizing git's own marker means there is
// nothing to keep in sync with git's spelling.
const dirtySuffix = "-dirty"

// TreeState is the state of the working tree the binary was built from.
//
// Three values rather than a bool: a binary with no stamped version does not
// know whether its tree was clean, and a bool would report that ignorance as
// "clean" — the same masquerade as a fake version, one field over.
type TreeState string

const (
	// TreeClean — built from a commit with no uncommitted changes.
	TreeClean TreeState = "clean"
	// TreeDirty — built from a working tree with uncommitted changes, so the
	// binary matches no commit and cannot be reproduced from one.
	TreeDirty TreeState = "dirty"
	// TreeUnknown — no version was stamped, so the tree state is not known.
	TreeUnknown TreeState = "unknown"
)

// Info is the build metadata one console binary carries. Its fields are
// reported verbatim by /health and by `--version`, so an operator compares a
// running service with a release artifact field by field.
type Info struct {
	// Version is `git describe --tags --always --dirty` as the build computed
	// it. At a release tag that is the tag; after it, the tag plus the commits
	// since and the abbreviated commit; with a modified tree, git's `-dirty`
	// suffix. Nothing rewrites it, so running the same git command in the source
	// tree yields the identical string — which is what makes it checkable.
	Version string
	// Revision is the full commit the binary was built from. Full, not
	// abbreviated: it is compared against `git rev-parse HEAD`, and a prefix
	// only narrows the search.
	Revision string
	// BuildTime is when the binary was linked, RFC 3339 in UTC. It separates
	// two builds of the same commit (a rebuilt artifact) from one.
	BuildTime string
	// Tree is the working tree state parsed out of Version.
	Tree TreeState
}

// Get returns the metadata the linker stamped, substituting Unstamped for
// anything it did not set.
func Get() Info {
	return Info{
		Version:   orUnstamped(version),
		Revision:  orUnstamped(revision),
		BuildTime: orUnstamped(buildTime),
		Tree:      treeState(version),
	}
}

// String renders the metadata as the single line `--version` prints and the
// startup log carries. Field names match /health's JSON keys so the two can be
// compared directly, and every field is always present — a partial report would
// let a missing revision read as "not applicable" instead of "unknown".
func (i Info) String() string {
	return fmt.Sprintf("version=%s revision=%s build_time=%s tree=%s",
		orUnstamped(i.Version), orUnstamped(i.Revision), orUnstamped(i.BuildTime), orUnknownTree(i.Tree))
}

// treeState parses `git describe --dirty`'s marker out of a version string.
//
// A version that carries no marker was built from a clean tree: git appends
// `-dirty` and nothing else. An empty version has no state to report and must
// not be reported as clean.
func treeState(v string) TreeState {
	switch {
	case v == "":
		return TreeUnknown
	case strings.HasSuffix(v, dirtySuffix):
		return TreeDirty
	default:
		return TreeClean
	}
}

func orUnstamped(v string) string {
	if v == "" {
		return Unstamped
	}
	return v
}

func orUnknownTree(t TreeState) TreeState {
	if t == "" {
		return TreeUnknown
	}
	return t
}
