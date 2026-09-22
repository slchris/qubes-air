package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeArchive creates a regular archive with a controlled modification time,
// so ordering assertions do not depend on how fast the test runs.
func writeArchive(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("archive payload"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

// stamp returns a distinct whole-second timestamp; distinctness must survive
// filesystems that only store seconds.
func stamp(hoursAgo int) time.Time {
	return time.Now().Add(-time.Duration(hoursAgo) * time.Hour).Truncate(time.Second)
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s should still exist: %v", filepath.Base(path), err)
	}
}

func mustBeGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s should have been deleted", filepath.Base(path))
	}
}

func baseNames(paths []string) []string {
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	return names
}

// reporter captures the report lines so tests can assert on what an operator
// would have seen in the journal.
type reporter struct {
	lines []string
}

func (r *reporter) logf(format string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *reporter) contains(substr string) bool {
	return strings.Contains(strings.Join(r.lines, "\n"), substr)
}

func TestPruneKeepsNewestArchives(t *testing.T) {
	dir := t.TempDir()
	oldest := writeArchive(t, dir, "a.qab", stamp(4))
	middle := writeArchive(t, dir, "b.qab", stamp(3))
	newer := writeArchive(t, dir, "c.qab", stamp(2))
	newest := writeArchive(t, dir, "d.qab", stamp(1))

	var logs reporter
	res, err := Prune(dir, 2, PruneOptions{Logf: logs.logf})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got, want := baseNames(res.Deleted), []string{"b.qab", "a.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Deleted = %v, want %v (newest first)", got, want)
	}
	if got, want := baseNames(res.Kept), []string{"d.qab", "c.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Kept = %v, want %v", got, want)
	}
	mustBeGone(t, oldest)
	mustBeGone(t, middle)
	mustExist(t, newer)
	mustExist(t, newest)
	if res.Failed != nil {
		t.Errorf("Failed = %v, want none", res.Failed)
	}
}

// The newest archive must survive even when its name sorts as the oldest:
// modification time is the retention order, not the name.
func TestPruneKeepsNewestDespiteNameOrder(t *testing.T) {
	dir := t.TempDir()
	newest := writeArchive(t, dir, "a.qab", stamp(1))
	oldest := writeArchive(t, dir, "z.qab", stamp(9))

	res, err := Prune(dir, 1, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got, want := baseNames(res.Deleted), []string{"z.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Deleted = %v, want %v", got, want)
	}
	mustExist(t, newest)
	mustBeGone(t, oldest)
}

func TestPruneAtKeepLimitDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		writeArchive(t, dir, "a.qab", stamp(3)),
		writeArchive(t, dir, "b.qab", stamp(2)),
		writeArchive(t, dir, "c.qab", stamp(1)),
	}

	var logs reporter
	res, err := Prune(dir, len(paths), PruneOptions{Logf: logs.logf})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Deleted) != 0 || len(res.Targets) != 0 {
		t.Errorf("Deleted = %v, Targets = %v, want both empty", baseNames(res.Deleted), baseNames(res.Targets))
	}
	if len(res.Kept) != len(paths) {
		t.Errorf("Kept = %v, want all %d archives", baseNames(res.Kept), len(paths))
	}
	for _, path := range paths {
		mustExist(t, path)
	}
	if !logs.contains("nothing to delete") {
		t.Errorf("a run that deletes nothing must say so, got %q", logs.lines)
	}
}

func TestPruneDeletesOnlyTheOldestOverLimit(t *testing.T) {
	dir := t.TempDir()
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	middle := writeArchive(t, dir, "b.qab", stamp(2))
	newest := writeArchive(t, dir, "c.qab", stamp(1))

	res, err := Prune(dir, 2, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got, want := baseNames(res.Deleted), []string{"a.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Deleted = %v, want exactly %v", got, want)
	}
	mustBeGone(t, oldest)
	mustExist(t, middle)
	mustExist(t, newest)
}

func TestPruneOnDirectoryWithoutArchives(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs reporter
	res, err := Prune(dir, 1, PruneOptions{Logf: logs.logf})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Deleted) != 0 || len(res.Kept) != 0 {
		t.Errorf("Deleted = %v, Kept = %v, want both empty", res.Deleted, res.Kept)
	}
	if !logs.contains("nothing to delete") {
		t.Errorf("expected the nothing-to-delete line, got %q", logs.lines)
	}
}

func TestPruneIgnoresFilesWithoutArchiveSuffix(t *testing.T) {
	dir := t.TempDir()
	// The temporary file is the oldest thing in the directory, so a policy
	// that ignored the suffix would delete it first.
	partial := writeArchive(t, dir, "c.qab.tmp", stamp(9))
	other := writeArchive(t, dir, "console.db", stamp(8))
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	newest := writeArchive(t, dir, "b.qab", stamp(1))

	res, err := Prune(dir, 1, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got, want := baseNames(res.Deleted), []string{"a.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Deleted = %v, want %v", got, want)
	}
	mustExist(t, partial)
	mustExist(t, other)
	mustBeGone(t, oldest)
	mustExist(t, newest)
}

func TestPruneIgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	staleDir := filepath.Join(dir, "stale.qab")
	if err := os.Mkdir(staleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	newest := writeArchive(t, dir, "b.qab", stamp(1))

	res, err := Prune(dir, 1, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got, want := baseNames(res.Deleted), []string{"a.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Deleted = %v, want %v", got, want)
	}
	mustExist(t, staleDir)
	mustBeGone(t, oldest)
	mustExist(t, newest)
}

// A symlink named like an archive is not an archive: it must neither be
// deleted nor count toward the keep total.
func TestPruneDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := writeArchive(t, dir, "a.qab", stamp(3))
	newest := writeArchive(t, dir, "b.qab", stamp(2))
	link := filepath.Join(dir, "link.qab")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var logs reporter
	res, err := Prune(dir, 2, PruneOptions{Logf: logs.logf})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Deleted) != 0 {
		t.Errorf("Deleted = %v, want none: only two regular archives exist", baseNames(res.Deleted))
	}
	if !logs.contains("nothing to delete") {
		t.Errorf("expected the nothing-to-delete line, got %q", logs.lines)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link.qab: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link.qab should still be a symlink, mode = %v", fi.Mode())
	}
	mustExist(t, target)
	mustExist(t, newest)
}

func TestPruneDryRunRemovesNothing(t *testing.T) {
	dir := t.TempDir()
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	middle := writeArchive(t, dir, "b.qab", stamp(2))
	newest := writeArchive(t, dir, "c.qab", stamp(1))

	var logs reporter
	res, err := Prune(dir, 1, PruneOptions{DryRun: true, Logf: logs.logf})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got, want := baseNames(res.Targets), []string{"b.qab", "a.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Targets = %v, want %v", got, want)
	}
	if len(res.Deleted) != 0 {
		t.Errorf("Deleted = %v, want none in a dry run", baseNames(res.Deleted))
	}
	for _, path := range []string{oldest, middle, newest} {
		mustExist(t, path)
	}
	for _, name := range []string{"a.qab", "b.qab"} {
		if !logs.contains("would delete " + filepath.Join(dir, name)) {
			t.Errorf("dry run did not report %s: %q", name, logs.lines)
		}
	}
	if logs.contains("c.qab") {
		t.Errorf("dry run reported the kept archive: %q", logs.lines)
	}
}

func TestPruneIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	newest := writeArchive(t, dir, "b.qab", stamp(1))

	first, err := Prune(dir, 1, PruneOptions{})
	if err != nil {
		t.Fatalf("first Prune: %v", err)
	}
	if got, want := baseNames(first.Deleted), []string{"a.qab"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first Deleted = %v, want %v", got, want)
	}

	var logs reporter
	second, err := Prune(dir, 1, PruneOptions{Logf: logs.logf})
	if err != nil {
		t.Fatalf("second Prune: %v", err)
	}
	if len(second.Deleted) != 0 || len(second.Targets) != 0 {
		t.Errorf("second run Deleted = %v, Targets = %v, want both empty",
			baseNames(second.Deleted), baseNames(second.Targets))
	}
	if !logs.contains("nothing to delete") {
		t.Errorf("second run must report nothing to delete, got %q", logs.lines)
	}
	mustBeGone(t, oldest)
	mustExist(t, newest)
}

func TestPruneEqualTimestampsBreakTiesByFileName(t *testing.T) {
	dir := t.TempDir()
	same := stamp(2)
	a := writeArchive(t, dir, "a.qab", same)
	b := writeArchive(t, dir, "b.qab", same)
	c := writeArchive(t, dir, "c.qab", same)

	res, err := Prune(dir, 1, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// Equal modification times: the greater name is treated as the newest.
	if got, want := baseNames(res.Kept), []string{"c.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Kept = %v, want %v", got, want)
	}
	if got, want := baseNames(res.Deleted), []string{"b.qab", "a.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Deleted = %v, want %v", got, want)
	}
	mustBeGone(t, a)
	mustBeGone(t, b)
	mustExist(t, c)
}

func TestPruneRejectsKeepBelowOne(t *testing.T) {
	for _, keep := range []int{0, -1} {
		dir := t.TempDir()
		oldest := writeArchive(t, dir, "a.qab", stamp(2))
		newest := writeArchive(t, dir, "b.qab", stamp(1))

		_, err := Prune(dir, keep, PruneOptions{})
		if !errors.Is(err, ErrKeepTooSmall) {
			t.Errorf("keep=%d: err = %v, want ErrKeepTooSmall", keep, err)
		}
		// The refusal must happen before anything is touched.
		mustExist(t, oldest)
		mustExist(t, newest)
	}
}

func TestPruneRejectsMissingOrNonDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	file := writeArchive(t, dir, "not-a-dir", stamp(1))

	for name, target := range map[string]string{
		"empty":       "",
		"missing":     filepath.Join(dir, "nope"),
		"regularfile": file,
	} {
		if _, err := Prune(target, 1, PruneOptions{}); !errors.Is(err, ErrNotDirectory) {
			t.Errorf("%s target: err = %v, want ErrNotDirectory", name, err)
		}
	}
	mustExist(t, file)
}

// A removal that fails must not hide what was already deleted, and it must not
// stop the remaining removals: the next run retries what is left.
func TestPruneReportsPartialFailure(t *testing.T) {
	dir := t.TempDir()
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	failing := writeArchive(t, dir, "b.qab", stamp(2))
	newest := writeArchive(t, dir, "c.qab", stamp(1))
	removeErr := errors.New("permission denied")

	var logs reporter
	res, err := Prune(dir, 1, PruneOptions{
		Logf: logs.logf,
		Remove: func(path string) error {
			if path == failing {
				return removeErr
			}
			return os.Remove(path)
		},
	})
	if !errors.Is(err, ErrPartialPrune) {
		t.Fatalf("err = %v, want ErrPartialPrune", err)
	}
	if got, want := baseNames(res.Deleted), []string{"a.qab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Deleted = %v, want %v", got, want)
	}
	if len(res.Failed) != 1 || res.Failed[0].Path != failing || !errors.Is(res.Failed[0].Err, removeErr) {
		t.Errorf("Failed = %+v, want one entry for %s", res.Failed, filepath.Base(failing))
	}
	// Both halves of the outcome must be readable from the error alone.
	for _, want := range []string{oldest, failing, "permission denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if logs.contains("nothing to delete") {
		t.Errorf("a partial failure is not a nothing-to-delete run: %q", logs.lines)
	}
	mustBeGone(t, oldest)
	mustExist(t, failing)
	mustExist(t, newest)
}
