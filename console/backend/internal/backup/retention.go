package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ArchiveSuffix is the file-name suffix that marks a console backup archive.
const ArchiveSuffix = ".qab"

// Errors callers switch on.
var (
	// ErrKeepTooSmall rejects a keep count that would delete every archive.
	// "Keep nothing" is not a retention policy: destroying the last copy is
	// something an operator does explicitly with rm, not something a timer
	// reaches by being handed a wrong number.
	ErrKeepTooSmall = errors.New("backup: keep must be at least 1")
	// ErrNotDirectory reports a prune target that is missing or not a
	// directory. There is deliberately no default: pruning the working
	// directory because a flag was left out is the accident this prevents.
	ErrNotDirectory = errors.New("backup: prune target must be an existing directory")
	// ErrPartialPrune reports that at least one selected archive survived.
	ErrPartialPrune = errors.New("backup: some archives could not be removed")
)

// PruneOptions controls Prune.
type PruneOptions struct {
	// DryRun selects and reports targets without removing anything.
	DryRun bool
	// Logf receives one line per target before that target is touched. A nil
	// Logf is allowed, but it leaves a deletion with no trace, so the command
	// always sets it.
	Logf func(format string, args ...any)
	// Remove deletes one archive; nil means os.Remove. It exists so the
	// partial-failure path can be exercised without depending on file modes or
	// on the test running unprivileged.
	Remove func(path string) error
}

// PruneFailure is one archive that could not be removed.
type PruneFailure struct {
	Path string
	Err  error
}

// PruneResult is what the retention policy selected and did. Every path list is
// newest first, so the last entry is the oldest archive involved.
type PruneResult struct {
	Kept    []string       // archives left in place (at most keep)
	Targets []string       // archives the policy selected for deletion
	Deleted []string       // targets that were removed (empty in a dry run)
	Failed  []PruneFailure // targets that could not be removed, with the reason
}

// Prune keeps the newest keep archives in dir and deletes the rest.
//
// Candidates are the regular files directly under dir whose name ends in
// ArchiveSuffix. Symbolic links are never followed: a link is not a regular
// file, so it is neither a candidate nor a reason to touch the file it points
// at. Modification time orders the candidates newest first; equal timestamps
// are broken by file name (greater name first) so that two runs over the same
// directory always agree on which archives are the oldest.
//
// Deletion is idempotent: once at most keep archives remain, a later run
// selects nothing and says so. A target that cannot be removed is recorded in
// Failed and does not stop the remaining removals; the returned error then
// wraps ErrPartialPrune and names both what was deleted and what failed,
// because "prune failed" alone does not tell an operator where it stopped.
func Prune(dir string, keep int, opts PruneOptions) (PruneResult, error) {
	var result PruneResult
	if keep < 1 {
		return result, fmt.Errorf("%w: got -keep %d", ErrKeepTooSmall, keep)
	}
	if dir == "" {
		return result, ErrNotDirectory
	}
	candidates, err := listArchives(dir)
	if err != nil {
		return result, err
	}
	if len(candidates) <= keep {
		result.Kept = candidates
		logf(opts, "nothing to delete: %d archive(s) in %s, keep=%d", len(candidates), dir, keep)
		return result, nil
	}
	result.Kept = candidates[:keep]
	result.Targets = candidates[keep:]

	remove := opts.Remove
	if remove == nil {
		remove = os.Remove
	}
	for _, path := range result.Targets {
		if opts.DryRun {
			logf(opts, "dry run: would delete %s", path)
			continue
		}
		// Report before removing: if the removal then fails, the one line
		// naming the target is already in the journal.
		logf(opts, "deleting %s", path)
		if err := remove(path); err != nil {
			result.Failed = append(result.Failed, PruneFailure{Path: path, Err: err})
			continue
		}
		result.Deleted = append(result.Deleted, path)
	}
	if opts.DryRun {
		logf(opts, "dry run: would delete %d archive(s), %d kept", len(result.Targets), len(result.Kept))
	} else {
		logf(opts, "deleted %d of %d selected archive(s), %d kept",
			len(result.Deleted), len(result.Targets), len(result.Kept))
	}
	if len(result.Failed) > 0 {
		return result, partialPruneError(result)
	}
	return result, nil
}

// listArchives returns the regular *.qab files directly under dir, newest
// first.
func listArchives(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNotDirectory, dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrNotDirectory, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("backup: read %s: %w", dir, err)
	}

	type candidate struct {
		path    string
		modTime time.Time
	}
	found := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ArchiveSuffix) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		// Lstat, not Stat: a symlink is not an archive even when its name
		// ends in .qab, so it must not be ranked with the real ones.
		fi, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			return nil, fmt.Errorf("backup: inspect %s: %w", path, lstatErr)
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		found = append(found, candidate{path: path, modTime: fi.ModTime()})
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].modTime.Equal(found[j].modTime) {
			return found[i].modTime.After(found[j].modTime)
		}
		return filepath.Base(found[i].path) > filepath.Base(found[j].path)
	})

	archives := make([]string, len(found))
	for i, c := range found {
		archives[i] = c.path
	}
	return archives, nil
}

func logf(opts PruneOptions, format string, args ...any) {
	if opts.Logf != nil {
		opts.Logf(format, args...)
	}
}

// partialPruneError spells out what was removed and what was not: a caller that
// only reads "prune failed" cannot tell whether the retention policy held.
func partialPruneError(result PruneResult) error {
	failed := make([]string, 0, len(result.Failed))
	for _, failure := range result.Failed {
		failed = append(failed, fmt.Sprintf("%s: %v", failure.Path, failure.Err))
	}
	return fmt.Errorf("%w: removed %d of %d target(s) (%s); failed: %s",
		ErrPartialPrune, len(result.Deleted), len(result.Targets),
		orNone(strings.Join(result.Deleted, ", ")), strings.Join(failed, "; "))
}

// orNone keeps "removed 0 of 3 target(s) ()" from reading like an omission.
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
