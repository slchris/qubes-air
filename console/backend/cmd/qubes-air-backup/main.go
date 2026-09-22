// Command qubes-air-backup creates and restores encrypted console database
// backups. See internal/backup for the format and its limits.
//
// The passphrase is read from QUBES_AIR_BACKUP_PASSPHRASE, never from argv: a
// command-line argument is visible to every process on the host via ps.
//
//	qubes-air-backup create  -db /rw/config/qubesair/qubes-air.db -out qubesair.qab
//	qubes-air-backup restore -db /rw/config/qubesair/qubes-air.db -in qubesair.qab -force
//
// A restore replaces the database; the console must be stopped first, and the
// keyring key (QUBES_AIR_ENCRYPTION_KEYS) must still be available or the
// restored credentials and CA key stay unreadable. See docs/disaster-recovery.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/slchris/qubes-air/console/internal/backup"
)

// buildVersion is overridden at link time (-X main.buildVersion=...).
var buildVersion = "dev"

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
		return fmt.Errorf("usage: qubes-air-backup <create|restore> [flags]")
	}
	switch args[0] {
	case "create":
		return runCreate(args[1:])
	case "restore":
		return runRestore(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q (want create or restore)", args[0])
	}
}

func runCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	db := fs.String("db", "", "path to the console SQLite database (required)")
	out := fs.String("out", "", "path to write the encrypted archive (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *out == "" {
		return fmt.Errorf("create requires -db and -out")
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

	if err := backup.Create(context.Background(), *db, passphrase, buildVersion, f); err != nil {
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

func requirePassphrase() (string, error) {
	p := os.Getenv(passphraseEnv)
	if p == "" {
		return "", fmt.Errorf("set %s (not passed on the command line, which other processes can read)", passphraseEnv)
	}
	return p, nil
}
