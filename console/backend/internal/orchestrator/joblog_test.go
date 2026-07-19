package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// A job id is used to build a filename. Should one ever arrive from a request,
// it must not be able to name a path outside the log directory.
func TestPathCannotEscapeTheLogDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "job-logs")
	s, err := NewJobLogStore(dir)
	if err != nil {
		t.Fatalf("NewJobLogStore: %v", err)
	}
	got := s.path("../../etc/passwd")
	if filepath.Dir(got) != dir {
		t.Errorf("path escaped the log dir: %q", got)
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
