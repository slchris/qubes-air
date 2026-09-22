package buildinfo

import (
	"strings"
	"testing"
)

// stamp sets the link-time variables for one test and restores them after. The
// tests drive the same three variables the linker writes, so what they exercise
// is the read path the binary actually runs — not a copy of it.
func stamp(t *testing.T, v, rev, built string) {
	t.Helper()
	oldVersion, oldRevision, oldBuildTime := version, revision, buildTime
	version, revision, buildTime = v, rev, built
	t.Cleanup(func() { version, revision, buildTime = oldVersion, oldRevision, oldBuildTime })
}

// TestGetReportsTheStampedValues covers the four combinations that occur in
// practice: a full stamp from a clean tree, a full stamp from a dirty tree (the
// developer build), no stamp at all (a plain `go build ./cmd/server`), and a
// partial stamp — which is what a renamed variable looks like, because -X
// matching nothing is silent.
func TestGetReportsTheStampedValues(t *testing.T) {
	const (
		rev = "0123456789abcdef0123456789abcdef01234567"
		bt  = "2026-09-22T20:15:00Z"
	)

	tests := []struct {
		name                string
		version, rev, built string
		want                Info
	}{
		{
			name:    "release tag, clean tree",
			version: "v1.2.3", rev: rev, built: bt,
			want: Info{Version: "v1.2.3", Revision: rev, BuildTime: bt, Tree: TreeClean},
		},
		{
			name:    "commits after the tag, clean tree",
			version: "v1.2.3-4-gabcdef", rev: rev, built: bt,
			want: Info{Version: "v1.2.3-4-gabcdef", Revision: rev, BuildTime: bt, Tree: TreeClean},
		},
		{
			name:    "dirty tree",
			version: "v1.2.3-4-gabcdef-dirty", rev: rev, built: bt,
			want: Info{Version: "v1.2.3-4-gabcdef-dirty", Revision: rev, BuildTime: bt, Tree: TreeDirty},
		},
		{
			name:    "no tags in the repository",
			version: "a70df74", rev: rev, built: bt,
			want: Info{Version: "a70df74", Revision: rev, BuildTime: bt, Tree: TreeClean},
		},
		{
			name:    "unstamped",
			version: "", rev: "", built: "",
			want: Info{Version: Unstamped, Revision: Unstamped, BuildTime: Unstamped, Tree: TreeUnknown},
		},
		{
			// -X silently does nothing when the variable name does not match, so a
			// rename can strand one field while the others keep working. The
			// stranded field must say unknown, not borrow the version next to it.
			name:    "partial stamp",
			version: "v1.2.3", rev: "", built: "",
			want: Info{Version: "v1.2.3", Revision: Unstamped, BuildTime: Unstamped, Tree: TreeClean},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stamp(t, tt.version, tt.rev, tt.built)

			if got := Get(); got != tt.want {
				t.Errorf("Get() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestUnstampedIsNotVersionShaped pins the choice of fallback. The constant this
// replaced was "0.1.0": a string an operator reads as an answer, in every build
// ever made. The replacement has to be recognizable as "nobody stamped this" on
// sight, and it must not be a string some other part of the system already uses
// to mean something else — "dev" is the agent's stamped default, so reusing it
// here would report a deliberate stage as if it were a build (G-H8).
func TestUnstampedIsNotVersionShaped(t *testing.T) {
	stamp(t, "", "", "")

	got := Get().Version
	if got != "unknown" {
		t.Fatalf("unstamped version = %q, want %q", got, "unknown")
	}
	for _, masquerade := range []string{"0.1.0", "dev", "v0.0.0", "latest"} {
		if got == masquerade {
			t.Errorf("unstamped version = %q, which reads as a real version", got)
		}
	}
}

// TestTreeStateParsesGitDescribeOutput covers the one derivation this package
// does. The inputs are the strings `git describe --dirty` actually produces:
// a tag, a tag plus commits and an abbreviated commit, the same with -dirty, and
// a bare abbreviated commit when the repository has no tags at all (which is
// this repository's state today).
func TestTreeStateParsesGitDescribeOutput(t *testing.T) {
	tests := []struct {
		version string
		want    TreeState
	}{
		{"v1.2.3", TreeClean},
		{"v1.2.3-4-gabcdef", TreeClean},
		{"v1.2.3-4-gabcdef-dirty", TreeDirty},
		{"v1.2.3-dirty", TreeDirty},
		{"a70df74", TreeClean},
		{"a70df74-dirty", TreeDirty},
		// The marker is a suffix, not a word: a tag that merely contains
		// "dirty" was still built from a clean tree.
		{"v1.2.3-dirtyx", TreeClean},
		{"dirty", TreeClean},
		{"dirty-v1.2.3", TreeClean},
		// Nothing stamped: no state to report, and "clean" would be a claim.
		{"", TreeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			if got := treeState(tt.version); got != tt.want {
				t.Errorf("treeState(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

// TestStringRendersOneLineForBothReaders pins the exact text `--version` prints
// and the startup log carries. An operator diffs this line against the same line
// from a release artifact, so the field names have to match /health's JSON keys
// and every field has to be present even when there is nothing to put in it.
func TestStringRendersOneLineForBothReaders(t *testing.T) {
	const (
		rev = "0123456789abcdef0123456789abcdef01234567"
		bt  = "2026-09-22T20:15:00Z"
	)

	tests := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "stamped, clean",
			info: Info{Version: "v1.2.3-4-gabcdef", Revision: rev, BuildTime: bt, Tree: TreeClean},
			want: "version=v1.2.3-4-gabcdef revision=" + rev + " build_time=" + bt + " tree=clean",
		},
		{
			name: "stamped, dirty",
			info: Info{Version: "v1.2.3-4-gabcdef-dirty", Revision: rev, BuildTime: bt, Tree: TreeDirty},
			want: "version=v1.2.3-4-gabcdef-dirty revision=" + rev + " build_time=" + bt + " tree=dirty",
		},
		{
			name: "unstamped",
			info: Info{Version: Unstamped, Revision: Unstamped, BuildTime: Unstamped, Tree: TreeUnknown},
			want: "version=unknown revision=unknown build_time=unknown tree=unknown",
		},
		{
			// A zero Info is what a caller that forgot Get() would hold. It must
			// render as unknown rather than as a line of empty fields, which
			// reads as "the revision is empty" instead of "nobody said".
			name: "zero value",
			info: Info{},
			want: "version=unknown revision=unknown build_time=unknown tree=unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGetDoesNotClaimCleanForAnUnstampedTree states the second half of the
// honesty rule: the tree field is not allowed to default to the reassuring
// value. A bool would have made "unknown" indistinguishable from "clean".
func TestGetDoesNotClaimCleanForAnUnstampedTree(t *testing.T) {
	stamp(t, "", "", "")

	if got := Get().Tree; got == TreeClean {
		t.Fatal("unstamped build reports tree=clean; an unknown tree must not read as a clean one")
	}
	if got := Get().Tree; got != TreeUnknown {
		t.Fatalf("unstamped tree = %q, want %q", got, TreeUnknown)
	}
	if !strings.Contains(Get().String(), "tree=unknown") {
		t.Fatalf("unstamped String() = %q, want it to contain tree=unknown", Get().String())
	}
}
