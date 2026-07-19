// Command rotate-key re-encrypts all stored credential secrets from their
// current encryption key version to the keyring's primary (highest) version.
//
// It is the fix for the rotation defect: previously, changing
// QUBES_AIR_ENCRYPTION_KEY made every existing credential undecryptable
// (AES-GCM authentication failure). With versioned keys, rotation is a
// three-step, no-downtime operation:
//
//  1. Add the new key alongside the old one via QUBES_AIR_ENCRYPTION_KEYS:
//
//     QUBES_AIR_ENCRYPTION_KEYS="v1:<OLD_32B_KEY>,v2:<NEW_32B_KEY>"
//
//     At this point the server can still decrypt v1 rows and encrypts new
//     rows with v2 (primary), so it is safe to deploy this before rotating.
//
//  2. Run this command with the SAME env, which decrypts every v1 row with the
//     v1 key and re-encrypts it with v2, inside a single transaction (atomic:
//     all-or-nothing, and idempotent/resumable — already-v2 rows are skipped).
//
//     rotate-key -config /path/config.yaml
//
//  3. Once `rotate-key -verify` reports 0 rows on old versions, drop the old
//     key: QUBES_AIR_ENCRYPTION_KEYS="v2:<NEW_32B_KEY>" (or the single-key form).
//
// Never delete an old key version from the keyring while any row still
// references it — decryption of those rows would fail permanently.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/repository"
)

func main() {
	log.SetFlags(0)

	configPath := flag.String("config", "", "Path to configuration file (YAML)")
	dsnFlag := flag.String("dsn", "", "Database DSN (overrides config/QUBES_AIR_DATABASE_DSN)")
	verify := flag.Bool("verify", false, "Report per-version row counts and exit (no changes)")
	flag.Parse()

	if err := run(*configPath, *dsnFlag, *verify); err != nil {
		log.Fatalf("rotate-key: %v", err)
	}
}

func run(configPath, dsnOverride string, verifyOnly bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	kr, err := cfg.Keyring()
	if err != nil {
		return fmt.Errorf("build keyring: %w", err)
	}

	if cfg.UsesDevEncryptionKey() {
		fmt.Fprintln(os.Stderr,
			"WARNING: rotating with the built-in development key. This is only meaningful for local testing.")
	}

	dsn := cfg.Database.DSN
	if dsnOverride != "" {
		dsn = dsnOverride
	}

	dbCfg := database.DefaultConfig()
	dbCfg.DSN = dsn
	db, err := database.New(dbCfg)
	if err != nil {
		return fmt.Errorf("open database %q: %w", dsn, err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	if verifyOnly {
		return reportVersions(ctx, db, kr.PrimaryVersion())
	}

	repo := repository.NewCredentialRepository(db, kr)

	log.Printf("rotating credentials to key version %d (configured versions: %v)...",
		kr.PrimaryVersion(), kr.Versions())

	stats, err := repo.RotateToPrimary(ctx)
	if err != nil {
		return fmt.Errorf("rotation aborted (no changes committed): %w", err)
	}

	log.Printf("rotation complete: %d total, %d re-encrypted to v%d, %d already current",
		stats.Total, stats.Reencrypted, stats.TargetVersion, stats.AlreadyCurrent)
	return nil
}

// reportVersions prints how many credential rows are at each key_version so an
// operator can confirm a rotation finished (0 rows on old versions) before
// dropping an old key.
func reportVersions(ctx context.Context, db *database.DB, primary int) error {
	rows, err := db.DB().QueryContext(ctx,
		`SELECT key_version, COUNT(*) FROM credentials GROUP BY key_version ORDER BY key_version`)
	if err != nil {
		return fmt.Errorf("querying versions: %w", err)
	}
	defer rows.Close()

	fmt.Printf("primary (current) key version: v%d\n", primary)
	fmt.Println("credential rows by key_version:")
	any := false
	for rows.Next() {
		var version, count int
		if err := rows.Scan(&version, &count); err != nil {
			return err
		}
		any = true
		marker := ""
		if version != primary {
			marker = "  <-- NOT rotated (old key still required)"
		}
		fmt.Printf("  v%d: %d%s\n", version, count, marker)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !any {
		fmt.Println("  (no credentials)")
	}
	return nil
}
