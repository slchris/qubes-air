// Package lockfile holds an advisory exclusive lock on a file for the whole
// life of a process.
//
// It exists so a second console cannot start against a database that a first
// console is already serving. The startup path reconciles state that belongs to
// a live process — it marks qubes error and unfinished jobs failed/unknown —
// so two instances sharing one SQLite file would each rewrite the other's
// in-flight work (docs/production-readiness-gaps.md, G-H7).
//
// The lock is flock(2) on an open descriptor:
//
//   - It is advisory and bound to the open file description, not to the path,
//     so the kernel releases it when that description's last descriptor is
//     closed — including when the process dies without any cleanup step.
//   - A leftover lock FILE therefore means nothing by itself, and a stale-lock
//     problem cannot exist. There is deliberately no pid-liveness check, no age
//     heuristic and no takeover path: whether a lock is held is decided by
//     flock alone, which is the only thing that can answer it correctly.
package lockfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Lock is an exclusive lock held on an open file descriptor.
//
// The descriptor must stay open, and reachable, for as long as the lock has to
// hold: closing it — through Release, through process exit, or through the
// runtime finalizer on an unreachable *os.File — drops the lock. Keeping the
// returned Lock in a variable that outlives the work (a deferred Release is
// enough, since the defer record keeps the value alive) is what holds it.
type Lock struct {
	path string
	file *os.File
}

// Path is the file the lock is held on, for logs and error messages. It is
// empty on a nil Lock, so callers can log the result of a failed acquisition
// unconditionally.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Acquire takes the exclusive, non-blocking lock on path, creating the file if
// it does not exist yet.
//
// A held lock is an error that names the path and, when the holder recorded
// one, the pid it wrote into the file. Acquisition never blocks: a console that
// silently waited for a running instance to exit would look like a hang, while
// the caller can turn this error into a refusal to start that says why.
func Acquire(path string) (*Lock, error) {
	if path == "" {
		return nil, errors.New("lockfile: lock path is empty")
	}
	// 0600: the file holds nothing but a pid, but it sits next to the database
	// and there is no reason to make it readable by everyone. Go opens it
	// O_CLOEXEC, so a child process cannot inherit the descriptor and keep the
	// lock alive after this process is gone.
	// The path is operator configuration — lock_file / QUBES_AIR_LOCK_FILE, or derived
	// from the operator's own DSN — and never request data, so there is no untrusted
	// input to traverse with. The file holds nothing but a pid.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- operator-supplied lock path, not request data
	if err != nil {
		return nil, fmt.Errorf("lockfile: open %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Read the holder before closing: the pid is the only part of the
		// refusal an operator cannot reconstruct from the path.
		holder := holderPID(f)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("lock file %s is held by %s: another console instance is "+
				"running against this database; stop it before starting a second one", path, holderDesc(holder))
		}
		return nil, fmt.Errorf("lockfile: lock %s: %w", path, err)
	}

	// The pid is diagnostic only — it feeds the refusal above. It is written
	// AFTER the lock is held, so a reader can never see the pid of a process
	// that does not hold the lock, and it is failure-fatal because a console
	// that cannot write next to its own database will not run for long anyway.
	if err := writePID(f); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, fmt.Errorf("lockfile: record holder pid in %s: %w", path, err)
	}

	return &Lock{path: path, file: f}, nil
}

// Release drops the lock and closes the descriptor.
//
// It is a no-op on a nil Lock and safe to call twice, so an acquisition path
// can defer it unconditionally. Closing alone would release the flock; the
// explicit unlock states the intent. Nothing needs to be undone on the paths
// that never reach Release — log.Fatalf exits the process, which closes every
// descriptor and releases the lock with it.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}

// writePID records this process's pid in the lock file, replacing whatever a
// previous holder left there.
func writePID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	return err
}

// holderPID reads back the pid the current holder wrote, or "" when the file
// holds nothing readable.
//
// It reads through the descriptor this process already opened instead of
// reopening the path, so the answer cannot come from a different file swapped
// in underneath. Content that is not a number is refused rather than pasted
// into an error message.
func holderPID(f *os.File) string {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	buf := make([]byte, 32)
	n, err := f.Read(buf)
	if n == 0 && err != nil {
		return ""
	}
	pid := strings.TrimSpace(string(buf[:n]))
	if _, err := strconv.Atoi(pid); err != nil {
		return ""
	}
	return pid
}

// holderDesc renders the holder for the refusal. An unreadable pid still has to
// produce an actionable sentence — the caller's next step is the same.
func holderDesc(pid string) string {
	if pid == "" {
		return "another process (its pid could not be read from the file)"
	}
	return "pid " + pid
}
