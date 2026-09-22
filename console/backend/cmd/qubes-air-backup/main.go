// Command qubes-air-backup creates, restores and prunes encrypted console
// database backups. See internal/backup for the format and its limits.
//
// The passphrase is read from QUBES_AIR_BACKUP_PASSPHRASE, never from argv: a
// command-line argument is visible to every process on the host via ps.
//
//	qubes-air-backup create  -db /rw/config/qubesair/qubes-air.db -out qubesair.qab
//	qubes-air-backup create  -db /rw/config/qubesair/qubes-air.db -out-dir /secure/offhost
//	qubes-air-backup restore -db /rw/config/qubesair/qubes-air.db -in qubesair.qab -force
//	qubes-air-backup prune   -dir /secure/offhost -keep 14
//	qubes-air-backup --version
//
// -out-dir names the archive itself (qubesair-<UTC stamp>.qab) so a systemd
// timer can run create without a shell to expand $(date). prune keeps the
// newest -keep archives and deletes the rest; it never runs without -dir.
//
// The build identity (version, revision, build_time, tree) comes from the same
// linker stamps the console carries (internal/buildinfo) and --version reports
// it before any subcommand, passphrase or database is touched — the release
// guard reads it back from the artifact exactly as it does for the console.
//
// A restore replaces the database; the console must be stopped first, and the
// keyring key (QUBES_AIR_ENCRYPTION_KEYS) must still be available or the
// restored credentials and CA key stay unreadable. See docs/disaster-recovery.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/slchris/qubes-air/console/internal/backup"
	"github.com/slchris/qubes-air/console/internal/buildinfo"
)

// appName is the name every --version line starts with, the same shape the
// console prints (`qubes-air-console version=…`).
const appName = "qubes-air-backup"

const passphraseEnv = "QUBES_AIR_BACKUP_PASSPHRASE" // #nosec G101 -- the environment variable NAME the operator sets, not a credential

func main() {
	log.SetFlags(0)
	log.SetPrefix("qubes-air-backup: ")
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: qubes-air-backup <create|restore|prune> [flags] (--version prints the build identity)")
	}
	// --version is answered before the subcommand dispatch and reads nothing but
	// the linker stamps: the release guard runs it on a runner with no
	// passphrase and no database, and an operator reads it off the artifact
	// before either exists.
	if args[0] == "--version" || args[0] == "-version" {
		fmt.Printf("%s %s\n", appName, buildinfo.Get())
		return nil
	}
	switch args[0] {
	case "create":
		return runCreate(args[1:])
	case "restore":
		return runRestore(args[1:])
	case "prune":
		return runPrune(args[1:], os.Stderr)
	default:
		return fmt.Errorf("unknown subcommand %q (want create, restore or prune)", args[0])
	}
}

func runCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	db := fs.String("db", "", "path to the console SQLite database (required)")
	out := fs.String("out", "", "path to write the encrypted archive (required unless -out-dir)")
	outDir := fs.String("out-dir", "",
		"directory to write qubesair-<UTC stamp>"+backup.ArchiveSuffix+" into (required unless -out)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return fmt.Errorf("create requires -db")
	}
	if (*out == "") == (*outDir == "") {
		return fmt.Errorf("create requires exactly one of -out or -out-dir")
	}
	if *outDir != "" {
		*out = filepath.Join(*outDir, defaultArchiveName(time.Now()))
	}
	passphrase, err := requirePassphrase()
	if err != nil {
		return err
	}

	// O_EXCL: refuse to silently overwrite an existing backup.
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer f.Close()

	// The archive header records which build wrote it, from the same linker stamp
	// --version prints: an archive, the binary that wrote it and the release
	// artifact can then be matched up without a second version scheme (which is
	// what -X main.buildVersion used to be).
	if err := backup.Create(context.Background(), *db, passphrase, buildinfo.Get().Version, f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync output: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote encrypted backup to %s\n", *out)
	return nil
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	db := fs.String("db", "", "path to restore the database to (required)")
	in := fs.String("in", "", "path to the encrypted archive (required)")
	force := fs.Bool("force", false, "overwrite an existing database file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *in == "" {
		return fmt.Errorf("restore requires -db and -in")
	}
	passphrase, err := requirePassphrase()
	if err != nil {
		return err
	}

	f, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	if err := backup.Restore(context.Background(), *db, passphrase, f, backup.RestoreOptions{Force: *force}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "restored database to %s\n", *db)
	return nil
}

// runPrune applies the retention policy: keep the newest -keep archives in
// -dir and delete the rest. Every line goes to w (stderr in production) as it
// happens, including the "nothing to delete" line a second run prints — the
// retention policy is only trustworthy if a no-op run says it was a no-op.
func runPrune(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	dir := fs.String("dir", "", "directory holding the "+backup.ArchiveSuffix+" archives (required, no default)")
	keep := fs.Int("keep", 0, "how many of the newest archives to keep (required, at least 1)")
	dryRun := fs.Bool("dry-run", false, "report what would be deleted without deleting it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("prune requires -dir (there is no default directory to prune)")
	}
	if _, err := backup.Prune(*dir, *keep, backup.PruneOptions{
		DryRun: *dryRun,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(w, format+"\n", args...)
		},
	}); err != nil {
		return err
	}
	return nil
}

// defaultArchiveName is the name -out-dir generates. It is UTC and
// second-resolution, so a timer's archives sort by name the same way they sort
// by modification time; a second run inside the same second therefore fails on
// the O_EXCL open instead of overwriting the archive it just wrote.
func defaultArchiveName(now time.Time) string {
	return "qubesair-" + now.UTC().Format("20060102T150405Z") + backup.ArchiveSuffix
}

func requirePassphrase() (string, error) {
	p := os.Getenv(passphraseEnv)
	if p == "" {
		return "", fmt.Errorf("set %s (not passed on the command line, which other processes can read)", passphraseEnv)
	}
	return p, nil
}
