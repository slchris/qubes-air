package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/buildinfo"
)

// linkStampPkg is the package whose variables the linker stamps through -X.
//
// It is repeated as a literal (the Makefile's CONSOLE_VERSION_PKG and
// scripts/build-release-binary.sh carry the same string) because that is the
// whole point: -X that matches no variable is silently ignored, so a rename
// that reaches only one of the three places ships a release binary reporting
// "unknown" with nothing red anywhere. Building the real binary here with the
// real flag is what makes such a rename fail.
const linkStampPkg = "github.com/slchris/qubes-air/console/internal/buildinfo"

// buildBackupBinary builds this command the way the Makefile and the release
// script do — `go build` from the package directory, with whatever -ldflags the
// caller passes — and returns the binary's path. Building the package under
// test rather than a stand-in is the point: a helper-only test cannot show that
// the flag reaches the linker at all.
func buildBackupBinary(t *testing.T, ldflags string) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "qubes-air-backup")
	args := []string{"build", "-o", bin}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, ".")

	out, err := exec.Command("go", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("go build %v: %v\n%s", args, err, out)
	}
	return bin
}

// runVersion executes the built binary's --version, which prints the stamp and
// exits before a subcommand runs — no passphrase, no database, no archive.
func runVersion(t *testing.T, bin string) string {
	t.Helper()

	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s --version: %v\n%s", bin, err, out)
	}
	return string(out)
}

// TestBuiltBinaryReportsTheLinkerStamps builds the backup CLI with the same
// three -X flags the Makefile and scripts/build-release-binary.sh pass, runs it,
// and asserts the exact line it prints. This is the only test that can observe
// the injection end to end: it fails if a variable is renamed, if the package
// path drifts, or if --version stops reading the stamps.
//
// The values are a dirty-tree describe, because that is the case that has to be
// distinguishable from a clean release build — and the case a developer's
// `make build` actually produces.
func TestBuiltBinaryReportsTheLinkerStamps(t *testing.T) {
	const (
		version   = "v1.2.3-4-gabcdef-dirty"
		revision  = "0123456789abcdef0123456789abcdef01234567"
		buildTime = "2026-09-22T20:15:00Z"
	)

	bin := buildBackupBinary(t, strings.Join([]string{
		"-X " + linkStampPkg + ".version=" + version,
		"-X " + linkStampPkg + ".revision=" + revision,
		"-X " + linkStampPkg + ".buildTime=" + buildTime,
	}, " "))

	want := "qubes-air-backup version=" + version +
		" revision=" + revision +
		" build_time=" + buildTime +
		" tree=dirty\n"

	if got := runVersion(t, bin); got != want {
		t.Errorf("--version printed %q, want %q", got, want)
	}
}

// TestPlainGoBuildReportsUnstamped builds the same command with no -ldflags at
// all — `go build ./cmd/qubes-air-backup` — and requires every field to say
// unknown. Before this change that build reported the constant "dev", which is
// indistinguishable from a real build in an archive header; the assertion that
// it must NOT print a version-shaped string is the regression guard.
func TestPlainGoBuildReportsUnstamped(t *testing.T) {
	bin := buildBackupBinary(t, "")

	const want = "qubes-air-backup version=unknown revision=unknown build_time=unknown tree=unknown\n"
	got := runVersion(t, bin)

	if got != want {
		t.Fatalf("--version printed %q, want %q", got, want)
	}
	for _, masquerade := range []string{"dev", "0.1.0"} {
		if strings.Contains(got, masquerade) {
			t.Errorf("unstamped --version printed %q, which reads as a real version", got)
		}
	}
}

// gitOutput runs git in the repo and returns the trimmed result, so a
// comparison can be made against the repository the test is running in rather
// than against a string copied from it.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()

	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// stampField reads one `name=value` field out of the --version line.
func stampField(t *testing.T, line, name string) string {
	t.Helper()

	for _, field := range strings.Fields(line) {
		if value, ok := strings.CutPrefix(field, name+"="); ok {
			return value
		}
	}
	t.Fatalf("%q carries no %s field", strings.TrimSpace(line), name)
	return ""
}

// TestMakefileStampsTheBinaryItBuilds runs the Makefile's own recipe and reads
// the artifact back.
//
// The two tests above build their own -ldflags, so they prove that the flag
// reaches the linker — but not that the Makefile still spells it the same way.
// The package path and all three variable names are repeated there, and a drift
// between the two is silent in both directions: `make build-backup` succeeds and
// ships a binary reporting "unknown" for the field that stopped matching, which
// is then pinned by digest into a disaster-recovery path. Nothing else in the
// tree asserts that recipe, so this is the only test that fails when
// CONSOLE_STAMP drifts.
//
// The expectations come from git rather than from literals, because that is the
// Makefile's actual contract: `version` is the verbatim `git describe` output
// and `revision` is HEAD, so the test checks the tree it runs in.
func TestMakefileStampsTheBinaryItBuilds(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// The recipe is what is under test, so it is executed rather than copied.
	// `make` is a documented prerequisite of this repository (AGENTS.md §3 runs
	// every gate through it), so a missing one is a failure and not a skip.
	if out, err := exec.Command("make", "-C", repoRoot, "build-backup").CombinedOutput(); err != nil {
		t.Fatalf("make build-backup: %v\n%s", err, out)
	}

	bin := filepath.Join(repoRoot, "console", "backend", "bin", "qubes-air-backup")
	line := runVersion(t, bin)

	if got, want := stampField(t, line, "version"), gitOutput(t, repoRoot, "describe", "--tags", "--always", "--dirty"); got != want {
		t.Errorf("make build-backup: version = %q, want this tree's `git describe --tags --always --dirty` = %q", got, want)
	}
	if got, want := stampField(t, line, "revision"), gitOutput(t, repoRoot, "rev-parse", "HEAD"); got != want {
		t.Errorf("make build-backup: revision = %q, want `git rev-parse HEAD` = %q", got, want)
	}
	if got := stampField(t, line, "build_time"); !strings.HasSuffix(got, "Z") {
		t.Errorf("make build-backup: build_time = %q, want the UTC `date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ` form", got)
	} else if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("make build-backup: build_time = %q is not an RFC 3339 timestamp: %v", got, err)
	}
	if got := stampField(t, line, "tree"); got != string(buildinfo.TreeClean) && got != string(buildinfo.TreeDirty) {
		t.Errorf("make build-backup: tree = %q, want %q or %q", got, buildinfo.TreeClean, buildinfo.TreeDirty)
	}
}
