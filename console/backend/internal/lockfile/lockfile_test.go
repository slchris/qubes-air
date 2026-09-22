package lockfile

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLockPath returns a lock path inside the test's own temp directory, so no
// test can be affected by another one's lock.
func newLockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "qubes-air.db.lock")
}

// TestAcquireRefusesASecondHolder is the single-instance property itself: while
// one holder has the lock, a second acquisition of the same path fails, and the
// error is actionable on its own because it is the entire diagnosis an operator
// gets from a unit that will not start.
//
// flock is bound to the open file description, so a second open + flock inside
// this one process conflicts exactly as a second process would; a test that
// needed a real second process would also be a test nobody runs.
func TestAcquireRefusesASecondHolder(t *testing.T) {
	path := newLockPath(t)

	first, err := Acquire(path)
	require.NoError(t, err)
	defer func() { _ = first.Release() }()

	second, err := Acquire(path)

	require.Error(t, err, "a second acquisition of %s must fail while the first is held", path)
	assert.Nil(t, second, "a refused acquisition must not hand back a lock")
	assert.Contains(t, err.Error(), path, "the refusal must name the lock file")
	assert.Contains(t, err.Error(), strconv.Itoa(os.Getpid()),
		"the refusal must name the holder pid written into the lock file")
}

// TestAcquireSucceedsAfterRelease proves the refusal is a lock and not a
// one-shot "does the file already exist" check: the same path must be usable
// again the moment the holder gives it back, with the file still on disk.
func TestAcquireSucceedsAfterRelease(t *testing.T) {
	path := newLockPath(t)

	first, err := Acquire(path)
	require.NoError(t, err)
	require.NoError(t, first.Release())

	second, err := Acquire(path)
	require.NoError(t, err, "the path must be reacquirable after Release")
	require.NoError(t, second.Release())
}

// TestAcquireAfterHolderDied pins the property that makes a leftover lock file
// harmless: the kernel releases an flock when the holder's descriptor goes
// away, so a process that dies without running any cleanup leaves nothing
// behind that stops the next start.
//
// The death is simulated by closing the descriptor directly — no Release, no
// explicit unlock, no cleanup step of any kind — which is also why the file's
// pid content must not be consulted: it is still there, and it is stale.
func TestAcquireAfterHolderDied(t *testing.T) {
	path := newLockPath(t)

	dead, err := Acquire(path)
	require.NoError(t, err)
	require.NoError(t, dead.file.Close())

	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "the file a dead holder left must still exist for this test to mean anything")

	next, err := Acquire(path)
	require.NoError(t, err, "a lock file left by a dead process must not block the next start")
	require.NoError(t, next.Release())
}

// TestLockFileNamesTheHolder covers the two things the refusal message depends
// on: the file carries the holder's pid, and Path reports the file the lock is
// actually held on. It also pins that Release is idempotent, because the
// startup path defers it unconditionally.
func TestLockFileNamesTheHolder(t *testing.T) {
	path := newLockPath(t)

	lock, err := Acquire(path)
	require.NoError(t, err)
	assert.Equal(t, path, lock.Path())

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid()), strings.TrimSpace(string(content)),
		"the lock file must name the process holding the lock")

	require.NoError(t, lock.Release())
	assert.NoError(t, lock.Release(), "Release must be safe to call twice")
}

// TestHeldLockWithoutReadablePIDStillRefuses covers the other half of the
// refusal: a lock file whose content is not a pid — truncated, or written by
// something else — must still produce a sentence that says what is wrong,
// rather than "held by pid " with nothing after it.
func TestHeldLockWithoutReadablePIDStillRefuses(t *testing.T) {
	path := newLockPath(t)

	holder, err := Acquire(path)
	require.NoError(t, err)
	defer func() { _ = holder.Release() }()

	// Rewriting the file does not touch the holder's flock: the lock is on the
	// open file description, not on the bytes.
	require.NoError(t, os.WriteFile(path, []byte("not-a-pid\n"), 0o600))

	second, err := Acquire(path)

	require.Error(t, err)
	assert.Nil(t, second)
	assert.Contains(t, err.Error(), path)
	assert.Contains(t, err.Error(), "pid could not be read")
}

// TestNilLockIsHarmless covers the one configuration that reaches it: an
// in-memory database gets no lock at all, and the startup path still defers
// Release unconditionally, so a nil Lock must neither panic nor error.
func TestNilLockIsHarmless(t *testing.T) {
	var lock *Lock

	assert.Equal(t, "", lock.Path())
	assert.NoError(t, lock.Release())
}

// TestAcquireRejectsEmptyPath pins the diagnostic for a caller bug. The guard
// it covers is not what makes an empty path fail — os.OpenFile("") fails on its
// own — it is what makes that failure say what is actually wrong instead of
// reporting a missing file.
func TestAcquireRejectsEmptyPath(t *testing.T) {
	lock, err := Acquire("")

	require.Error(t, err)
	assert.Nil(t, lock)
	assert.Contains(t, err.Error(), "lock path is empty")
}

// TestRefusalNamesThePIDWrittenInTheFile closes the one gap an in-process test
// leaves open: TestLockFileNamesTheHolder compares the file against this
// process's own pid, so it cannot tell a message that reads the file from one
// that reports the caller's pid. Here the file is made to name a different
// process, which is what the refusal has to repeat back.
//
// In production the two coincide by construction — Acquire writes the pid only
// after it holds the lock — so this is the only way to show the message is
// actually sourced from the file.
func TestRefusalNamesThePIDWrittenInTheFile(t *testing.T) {
	path := newLockPath(t)

	holder, err := Acquire(path)
	require.NoError(t, err)
	defer func() { _ = holder.Release() }()

	// A valid pid that is not this process's. Rewriting the file does not touch
	// the holder's lock: the lock is on the open file description.
	const otherPID = "999999"
	require.NotEqual(t, otherPID, strconv.Itoa(os.Getpid()))
	require.NoError(t, os.WriteFile(path, []byte(otherPID+"\n"), 0o600))

	_, err = Acquire(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "pid "+otherPID,
		"the refusal must name the pid in the lock file, not the caller's own pid")
}
