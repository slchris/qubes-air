package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/slchris/qubes-air/console/internal/provider"
)

// Job logs: what the provider operation reported, while it was happening.
//
// A provision takes 15-25 minutes on real hardware. Before this, the only
// record was Job.Error, written once the operation had already ended — so for
// the whole run the console could say nothing beyond "running", and a failure
// arrived as one wall of text with no indication of how far it had got. The
// operator's view of a long provision was indistinguishable from a hung one.
//
// Written to a file per job rather than accumulated in memory: the output of a
// large provision is unbounded, several jobs' worth would sit in the process
// forever, and a file survives a console restart mid-operation — which is
// exactly when someone wants to know what happened.

// logSinkKey carries the writer for the job currently executing.
//
// Passed through the context rather than added to the Executor interface: the
// interface takes a qube name, not a job, and threading an id through every
// method to reach one io.Writer would change five signatures for a concern none
// of them have. It also does not rely on the Runner being single-threaded,
// which it is today and which nothing enforces.
type logSinkKey struct{}

// WithLogSink returns a context whose provider operations copy their output
// to w in addition to buffering it.
func WithLogSink(ctx context.Context, w io.Writer) context.Context {
	if w == nil {
		return ctx
	}
	// Both keys: the executor's own output reader uses logSink, and provider
	// adapters read the writer through the provider package so they do not have
	// to import the orchestrator.
	return provider.WithLogWriter(context.WithValue(ctx, logSinkKey{}, w), w)
}

// logSinkFrom returns the sink for this context, or nil.
func logSinkFrom(ctx context.Context) io.Writer {
	w, _ := ctx.Value(logSinkKey{}).(io.Writer)
	return w
}

// JobLogStore stores and serves the output of a job.
type JobLogStore struct {
	dir string
}

// NewJobLogStore writes logs under dir, creating it if needed.
func NewJobLogStore(dir string) (*JobLogStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("job log dir is empty")
	}
	// 0700: the output includes provider responses that describe infrastructure,
	// which is not a credential store but is not public either.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create job log dir %q: %w", dir, err)
	}
	return &JobLogStore{dir: dir}, nil
}

// path is the log file for a job id.
//
// filepath.Base defends the path against an id that is not what the caller
// thinks it is: job ids are generated internally today, but this turns a future
// id that arrives from a request into a harmless filename rather than a write
// outside the directory.
func (s *JobLogStore) path(jobID string) string {
	return filepath.Join(s.dir, filepath.Base(jobID)+".log")
}

// Create opens the log for writing. The caller closes it.
func (s *JobLogStore) Create(jobID string) (*os.File, error) {
	return os.OpenFile(s.path(jobID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

// ReadFrom returns the log bytes from offset, and the new offset.
//
// Offset-based rather than streaming: the client polls with the offset it last
// saw and gets whatever has been appended since. That reads the same whether
// the job is running, finished, or ran before the console last restarted, and
// it needs no connection held open for the twenty minutes an apply can take.
//
// A missing file is not an error — a job that has not started writing yet is
// normal, and reporting it as a failure would make the UI show an error for a
// job that is merely young.
func (s *JobLogStore) ReadFrom(jobID string, offset int64, max int64) ([]byte, int64, error) {
	f, err := os.Open(s.path(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, offset, nil
		}
		return nil, offset, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	size := info.Size()

	// A truncated file means the job was re-run and the log restarted. Reading
	// from a stale offset would return nothing forever, so rewind rather than
	// leave the client stuck on an offset past the end.
	if offset > size {
		offset = 0
	}
	if offset == size {
		return nil, offset, nil
	}

	n := size - offset
	if max > 0 && n > max {
		n = max
	}
	buf := make([]byte, n)
	read, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, offset, err
	}
	return buf[:read], offset + int64(read), nil
}
