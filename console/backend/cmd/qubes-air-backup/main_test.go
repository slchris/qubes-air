package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/backup"
	"github.com/slchris/qubes-air/console/internal/database"
)

// writeArchive creates a regular archive with a controlled modification time
// so the prune ordering assertions do not depend on test speed.
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

func TestRunPruneDryRunOnlyReports(t *testing.T) {
	dir := t.TempDir()
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	middle := writeArchive(t, dir, "b.qab", stamp(2))
	newest := writeArchive(t, dir, "c.qab", stamp(1))

	var stderr bytes.Buffer
	if err := runPrune([]string{"-dir", dir, "-keep", "1", "-dry-run"}, &stderr); err != nil {
		t.Fatalf("runPrune: %v", err)
	}
	for _, path := range []string{oldest, middle, newest} {
		mustExist(t, path)
	}
	for _, name := range []string{"a.qab", "b.qab"} {
		if !strings.Contains(stderr.String(), "would delete "+filepath.Join(dir, name)) {
			t.Errorf("dry run did not report %s:\n%s", name, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "c.qab") {
		t.Errorf("dry run reported the archive it would keep:\n%s", stderr.String())
	}
}

func TestRunPruneIsIdempotentAcrossTwoRuns(t *testing.T) {
	dir := t.TempDir()
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	newest := writeArchive(t, dir, "b.qab", stamp(1))

	if err := runPrune([]string{"-dir", dir, "-keep", "1"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first runPrune: %v", err)
	}
	mustBeGone(t, oldest)

	var stderr bytes.Buffer
	if err := runPrune([]string{"-dir", dir, "-keep", "1"}, &stderr); err != nil {
		t.Fatalf("second runPrune: %v", err)
	}
	if !strings.Contains(stderr.String(), "nothing to delete") {
		t.Errorf("second run must say there was nothing to delete:\n%s", stderr.String())
	}
	mustExist(t, newest)
}

func TestRunPruneRequiresDir(t *testing.T) {
	var stderr bytes.Buffer
	err := runPrune([]string{"-keep", "3"}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "-dir") {
		t.Fatalf("err = %v, want a message about -dir", err)
	}
}

func TestRunPruneRejectsKeepBelowOne(t *testing.T) {
	dir := t.TempDir()
	oldest := writeArchive(t, dir, "a.qab", stamp(3))
	newest := writeArchive(t, dir, "b.qab", stamp(1))

	for _, keep := range []string{"0", "-1"} {
		err := runPrune([]string{"-dir", dir, "-keep", keep}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "at least 1") {
			t.Errorf("-keep %s: err = %v, want a refusal to keep nothing", keep, err)
		}
	}
	mustExist(t, oldest)
	mustExist(t, newest)
}

func TestRunPruneRejectsMissingOrNonDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "console.db")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, target := range map[string]string{
		"missing":     filepath.Join(dir, "nope"),
		"regularfile": file,
	} {
		err := runPrune([]string{"-dir", target, "-keep", "1"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "must be an existing directory") {
			t.Errorf("%s target: err = %v, want a refusal", name, err)
		}
	}
	mustExist(t, file)
}

func TestDefaultArchiveName(t *testing.T) {
	now := time.Date(2026, 9, 22, 15, 4, 5, 0, time.UTC)
	name := defaultArchiveName(now)
	if want := "qubesair-20260922T150405Z" + backup.ArchiveSuffix; name != want {
		t.Errorf("defaultArchiveName = %q, want %q", name, want)
	}
	// A local-time input must still produce the same UTC stamp: the archive
	// name has to be stable no matter which timezone the timer runs in.
	other := time.Date(2026, 9, 22, 23, 4, 5, 0, time.FixedZone("UTC+8", 8*3600))
	if name != defaultArchiveName(other) {
		t.Errorf("defaultArchiveName is not timezone-independent: %q vs %q", name, defaultArchiveName(other))
	}
}

func TestRunCreateRequiresExactlyOneOutputFlag(t *testing.T) {
	db := filepath.Join(t.TempDir(), "console.db")
	if err := os.WriteFile(db, []byte("claims to be a database"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runCreate([]string{"-db", db})
	if err == nil || !strings.Contains(err.Error(), "exactly one of -out or -out-dir") {
		t.Fatalf("no output flag: err = %v, want a message about -out/-out-dir", err)
	}
	// The refusal comes from flag validation, not from a later step that
	// happens to fail first.
	if strings.Contains(err.Error(), passphraseEnv) {
		t.Errorf("the output-flag check ran after the passphrase check: %v", err)
	}
	err = runCreate([]string{"-db", db, "-out", db, "-out-dir", filepath.Dir(db)})
	if err == nil || !strings.Contains(err.Error(), "exactly one of -out or -out-dir") {
		t.Fatalf("both output flags: err = %v, want a message about -out/-out-dir", err)
	}
}

// The documented drill is create -> restore to a separate temporary path ->
// open the result; running it here keeps the -out-dir name, the O_EXCL 0600
// file mode and the round trip covered by a test rather than by a checklist.
func TestRunCreateOutDirThenRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "console.db")
	createTestDB(t, dbPath)

	outDir := filepath.Join(dir, "archives")
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(passphraseEnv, "test passphrase")

	if err := runCreate([]string{"-db", dbPath, "-out-dir", outDir}); err != nil {
		t.Fatalf("runCreate -out-dir: %v", err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read archive dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive dir holds %d entries, want 1", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "qubesair-") || !strings.HasSuffix(name, backup.ArchiveSuffix) {
		t.Errorf("archive name = %q, want qubesair-*%s", name, backup.ArchiveSuffix)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("archive mode = %o, want 600", perm)
	}

	restored := filepath.Join(dir, "restored.db")
	if err := runRestore([]string{"-db", restored, "-in", filepath.Join(outDir, name)}); err != nil {
		t.Fatalf("runRestore: %v", err)
	}
	cfg := database.DefaultConfig()
	cfg.DSN = restored
	db, err := database.New(cfg)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer db.Close()
	if v, err := db.UserVersion(); err != nil || v != database.SchemaVersion {
		t.Errorf("restored schema version = %d (err %v), want %d", v, err, database.SchemaVersion)
	}
}

// createTestDB writes a real console database so create/restore exercise the
// same SQLite path the console uses.
func createTestDB(t *testing.T, path string) {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = path
	db, err := database.New(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
}
