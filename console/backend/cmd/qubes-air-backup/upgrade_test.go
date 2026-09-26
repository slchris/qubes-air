package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/database/dbtest"
)

// The recovery path an operator actually takes after an upgrade: an archive a
// v2 console wrote is restored by this build and opened by it. The archive
// keeps the version it was taken at, and opening the restored file upgrades it
// to this build's schema with the fleet rows intact and an empty audit trail.
func TestRestoreOfAV2ArchiveUpgradesOnOpen(t *testing.T) {
	dir := t.TempDir()
	source := dbtest.WriteV2Fixture(t)
	t.Setenv(passphraseEnv, "test passphrase")

	archive := filepath.Join(dir, "v2.qab")
	if err := runCreate([]string{"-db", source, "-out", archive}); err != nil {
		t.Fatalf("runCreate: %v", err)
	}
	restored := filepath.Join(dir, "restored.db")
	if err := runRestore([]string{"-db", restored, "-in", archive}); err != nil {
		t.Fatalf("runRestore: %v", err)
	}

	cfg := database.DefaultConfig()
	cfg.DSN = restored
	db, err := database.New(cfg)
	if err != nil {
		t.Fatalf("open restored v2 database: %v", err)
	}
	defer db.Close()
	if v, err := db.UserVersion(); err != nil || v != database.SchemaVersion {
		t.Errorf("restored schema version = %d (err %v), want %d", v, err, database.SchemaVersion)
	}
	ctx := context.Background()
	var zones, auditRows int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM zones WHERE id = 'zone-pve-lab'`).Scan(&zones); err != nil {
		t.Fatalf("count zones: %v", err)
	}
	if zones != 1 {
		t.Errorf("restored database lost the v2 zone row")
	}
	dbtest.AssertV2RowsPreserved(t, db.DB())
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&auditRows); err != nil {
		t.Fatalf("the upgrade must create audit_events: %v", err)
	}
	if auditRows != 0 {
		t.Errorf("audit_events holds %d rows after an upgrade, want 0", auditRows)
	}
}
