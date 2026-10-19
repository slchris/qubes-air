package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *JobLogStore {
	t.Helper()
	s, err := NewJobLogStore(filepath.Join(t.TempDir(), "job-logs"))
	if err != nil {
		t.Fatalf("NewJobLogStore: %v", err)
	}
	return s
}

// The whole point is reading output while the job is still producing it, so a
// read must return what has been written so far rather than waiting for a close.
func TestReadFromReturnsPartialOutputWhileWriting(t *testing.T) {
	s := newStore(t)
	f, err := s.Create("job-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString("first\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, off, err := s.ReadFrom("job-1", 0, 0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if string(data) != "first\n" {
		t.Errorf("data = %q, want %q", data, "first\n")
	}

	// A second read from the returned offset must yield only what came after.
	if _, err := f.WriteString("second\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _, err = s.ReadFrom("job-1", off, 0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if string(data) != "second\n" {
		t.Errorf("incremental data = %q, want %q", data, "second\n")
	}
}

// A job that has not written yet is normal, not an error: reporting it as one
// would make the UI show a failure for a job that is merely young.
func TestReadFromMissingLogIsNotAnError(t *testing.T) {
	s := newStore(t)
	data, off, err := s.ReadFrom("never-ran", 0, 0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if len(data) != 0 || off != 0 {
		t.Errorf("got data=%q off=%d, want empty", data, off)
	}
}

// Re-running a job truncates its log. A client still holding the old offset
// must not be stuck past the end, seeing nothing forever.
func TestReadFromRewindsWhenTruncated(t *testing.T) {
	s := newStore(t)
	f, _ := s.Create("job-2")
	_, _ = f.WriteString("a long first run\n")
	_, off, _ := s.ReadFrom("job-2", 0, 0)
	_ = f.Close()

	f2, err := s.Create("job-2") // O_TRUNC
	if err != nil {
		t.Fatalf("re-Create: %v", err)
	}
	defer func() { _ = f2.Close() }()
	_, _ = f2.WriteString("short\n")

	data, _, err := s.ReadFrom("job-2", off, 0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if string(data) != "short\n" {
		t.Errorf("after truncation got %q, want the new run's output", data)
	}
}

func TestReadFromRespectsMax(t *testing.T) {
	s := newStore(t)
	f, _ := s.Create("job-3")
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(strings.Repeat("x", 100))

	data, off, err := s.ReadFrom("job-3", 0, 10)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if len(data) != 10 || off != 10 {
		t.Errorf("got len=%d off=%d, want 10/10", len(data), off)
	}
}

// The job id names a file, so it is validated as an allowlist before it reaches
// a path: an id that arrives from a request must never name anything but one
// log file. Both length bounds and the full allowed alphabet are pinned here.
func TestValidJobID(t *testing.T) {
	valid := []string{
		"a",                     // shortest allowed
		"Z",                     // upper case
		"0",                     // digit
		"_",                     // underscore
		"-",                     // hyphen
		"job-1",                 // the shape the tests use
		"Ab_9-",                 // mixed
		strings.Repeat("a", 64), // longest allowed
	}
	for _, id := range valid {
		if !validJobID(id) {
			t.Errorf("expected %q to be valid", id)
		}
	}

	invalid := []string{
		"",                      // empty
		strings.Repeat("a", 65), // one past the bound
		".",
		"..",
		"../etc/passwd", // traversal
		"a/b",           // path separator
		"a\\b",          // path separator
		"a.b",           // dot is a traversal primitive
		"a b",           // space
		"a\tb",          // tab
		"a\nb",          // newline
		"a\x00b",        // NUL truncation
		"a;b",
		"a$b",
		"名字",   // non-ascii
		"café", // non-ascii
	}
	for _, id := range invalid {
		if validJobID(id) {
			t.Errorf("expected %q to be REJECTED", id)
		}
	}
}

// Rejecting an id must happen before a file is opened, and the error must say
// which kind of failure it was.
func TestCreateRejectsInvalidJobID(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"", "a/b", "..", "a b", strings.Repeat("a", 65)} {
		f, err := s.Create(id)
		if err == nil {
			if f != nil {
				_ = f.Close()
			}
			t.Errorf("Create(%q): expected an error", id)
			continue
		}
		var invalid *ErrInvalidJobID
		if !errors.As(err, &invalid) {
			t.Errorf("Create(%q): expected ErrInvalidJobID, got %v", id, err)
		}
	}
}

func TestReadFromRejectsInvalidJobID(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"", "a/b", "..", "a b", strings.Repeat("a", 65)} {
		data, off, err := s.ReadFrom(id, 0, 0)
		if err == nil {
			t.Errorf("ReadFrom(%q): expected an error", id)
			continue
		}
		var invalid *ErrInvalidJobID
		if !errors.As(err, &invalid) {
			t.Errorf("ReadFrom(%q): expected ErrInvalidJobID, got %v", id, err)
		}
		if len(data) != 0 || off != 0 {
			t.Errorf("ReadFrom(%q): got data=%q off=%d, want empty/0", id, data, off)
		}
	}
}

// An escaping id must not reach the filesystem: path is the single choke point
// and refuses it rather than building a name outside the log directory.
func TestPathRejectsEscapingJobID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "job-logs")
	s, err := NewJobLogStore(dir)
	if err != nil {
		t.Fatalf("NewJobLogStore: %v", err)
	}
	got, err := s.path("../../etc/passwd")
	if err == nil {
		t.Errorf("path(%q) = %q, want an error", "../../etc/passwd", got)
	}
	if got != "" {
		t.Errorf("path returned %q alongside the error, want empty", got)
	}
}

// Valid ids at both length bounds must still round-trip, so the guard does not
// reject ids that real traffic (a UUID) actually produces.
func TestJobLogRoundTripsAtLengthBounds(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"a", strings.Repeat("A", 64)} {
		f, err := s.Create(id)
		if err != nil {
			t.Fatalf("Create(%q): %v", id, err)
		}
		if _, err := f.WriteString("hello\n"); err != nil {
			t.Fatalf("write(%q): %v", id, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close(%q): %v", id, err)
		}
		data, _, err := s.ReadFrom(id, 0, 0)
		if err != nil {
			t.Fatalf("ReadFrom(%q): %v", id, err)
		}
		if string(data) != "hello\n" {
			t.Errorf("ReadFrom(%q) = %q, want %q", id, data, "hello\n")
		}
	}
}

// The sink is how a running apply becomes watchable; a context without one must
// still work, since that is the configuration where logging is switched off.
func TestLogSinkRoundTrip(t *testing.T) {
	if logSinkFrom(context.Background()) != nil {
		t.Error("a bare context reported a sink")
	}
	if got := WithLogSink(context.Background(), nil); logSinkFrom(got) != nil {
		t.Error("WithLogSink(nil) installed a sink")
	}
	f, err := os.CreateTemp(t.TempDir(), "sink")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = f.Close() }()
	if logSinkFrom(WithLogSink(context.Background(), f)) == nil {
		t.Error("sink did not survive the context")
	}
}

// Exists separates "never wrote a log" from "wrote one that is now gone": both
// read as empty, but only one is a fault the API should explain.
func TestExistsReportsPresence(t *testing.T) {
	s := newStore(t)
	ok, err := s.Exists("job-1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if ok {
		t.Error("a job with no file reported an existing log")
	}

	f, err := s.Create("job-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ok, err = s.Exists("job-1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("a written log was not reported")
	}

	// A directory named like a log is not a log; reporting it as one would let
	// the handler stream an empty log over a storage fault.
	if err := os.Mkdir(filepath.Join(s.dir, "job-2.log"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	ok, err = s.Exists("job-2")
	if err != nil {
		t.Fatalf("Exists(dir): %v", err)
	}
	if ok {
		t.Error("a directory was reported as a job log")
	}

	if _, err := s.Exists("../escape"); err == nil {
		t.Error("Exists accepted an escaping id")
	} else {
		var invalid *ErrInvalidJobID
		if !errors.As(err, &invalid) {
			t.Errorf("Exists error = %v, want ErrInvalidJobID", err)
		}
	}
}

func writeLogAt(t *testing.T, s *JobLogStore, name string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(s.dir, name)
	if err := os.WriteFile(p, []byte("output\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return p
}

// Prune is the retention boundary: it must remove exactly the old, valid log
// files and touch nothing else in the directory.
func TestPruneRemovesOnlyOldValidLogFiles(t *testing.T) {
	s := newStore(t)
	old := time.Now().Add(-2 * time.Hour)
	cutoff := time.Now().Add(-time.Hour)

	oldB := writeLogAt(t, s, "job-b.log", old)
	oldA := writeLogAt(t, s, "job-a.log", old)
	fresh := writeLogAt(t, s, "job-fresh.log", time.Now())
	stray := writeLogAt(t, s, "notes.txt", old)
	badName := writeLogAt(t, s, "bad name.log", old)
	dir := filepath.Join(s.dir, "olddir.log")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	pruned, err := s.Prune(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	want := []string{"job-a", "job-b"}
	if len(pruned) != len(want) || pruned[0] != want[0] || pruned[1] != want[1] {
		t.Errorf("pruned = %v, want %v (sorted)", pruned, want)
	}

	for _, p := range []string{oldA, oldB} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived prune", p)
		}
	}
	for _, p := range []string{fresh, stray, badName, dir} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}

// A shut-down console must stop pruning without deleting something the caller
// can no longer record on its job row.
func TestPruneHonorsCanceledContext(t *testing.T) {
	s := newStore(t)
	p := writeLogAt(t, s, "job-old.log", time.Now().Add(-2*time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pruned, err := s.Prune(ctx, time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prune error = %v, want context.Canceled", err)
	}
	if len(pruned) != 0 {
		t.Errorf("Prune removed %v after cancellation", pruned)
	}
	if _, statErr := os.Stat(p); statErr != nil {
		t.Errorf("canceled prune removed the file: %v", statErr)
	}
}
