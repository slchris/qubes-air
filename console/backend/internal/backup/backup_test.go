package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
)

// newTestDB creates a real console database at path with one zone row.
func newTestDB(t *testing.T, path string) *database.DB {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = path
	db, err := database.New(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	if _, err := db.DB().Exec(
		`INSERT INTO zones (id, name, type, status, config, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"zone-1", "infra-qa", "proxmox", "connected", `{}`, now, now,
	); err != nil {
		t.Fatalf("insert zone: %v", err)
	}
	return db
}

func TestCreateRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	newTestDB(t, srcPath)

	var archive bytes.Buffer
	const pass = "correct horse battery staple"
	if err := Create(context.Background(), srcPath, pass, "test-build", &archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if archive.Len() == 0 {
		t.Fatal("archive is empty")
	}

	dstPath := filepath.Join(dir, "restored.db")
	if err := Restore(context.Background(), dstPath, pass, &archive, RestoreOptions{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	cfg := database.DefaultConfig()
	cfg.DSN = dstPath
	restored, err := database.New(cfg)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer restored.Close()

	var name string
	if err := restored.DB().QueryRow(`SELECT name FROM zones WHERE id = ?`, "zone-1").Scan(&name); err != nil {
		t.Fatalf("read restored row: %v", err)
	}
	if name != "infra-qa" {
		t.Errorf("restored name = %q, want infra-qa", name)
	}
	if v, err := restored.UserVersion(); err != nil || v != database.SchemaVersion {
		t.Errorf("restored schema version = %d (err %v), want %d", v, err, database.SchemaVersion)
	}
}

func TestRestoreRejectsWrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	newTestDB(t, srcPath)

	var archive bytes.Buffer
	if err := Create(context.Background(), srcPath, "right", "", &archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := Restore(context.Background(), filepath.Join(dir, "out.db"), "wrong", &archive, RestoreOptions{})
	if !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("err = %v, want ErrBadPassphrase", err)
	}
}

func TestRestoreRejectsTamperedArchive(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	newTestDB(t, srcPath)

	var archive bytes.Buffer
	if err := Create(context.Background(), srcPath, "pass", "", &archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Flip a byte in the ciphertext tail; GCM must reject it.
	raw := archive.Bytes()
	raw[len(raw)-1] ^= 0xff

	err := Restore(context.Background(), filepath.Join(dir, "out.db"), "pass", bytes.NewReader(raw), RestoreOptions{})
	if !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("tampered archive: err = %v, want ErrBadPassphrase", err)
	}
}

func TestRestoreRefusesExistingTargetWithoutForce(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	newTestDB(t, srcPath)

	var archive bytes.Buffer
	if err := Create(context.Background(), srcPath, "pass", "", &archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	target := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(target, []byte("do not clobber"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Restore(context.Background(), target, "pass", bytes.NewReader(archive.Bytes()), RestoreOptions{})
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("err = %v, want ErrTargetExists", err)
	}
	// The existing file must be untouched.
	got, _ := os.ReadFile(target)
	if string(got) != "do not clobber" {
		t.Errorf("existing target was modified: %q", got)
	}

	if err := Restore(context.Background(), target, "pass", bytes.NewReader(archive.Bytes()), RestoreOptions{Force: true}); err != nil {
		t.Fatalf("force restore: %v", err)
	}
}

func TestRestoreRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()

	// The header is checked before decryption, so the payload only needs to be
	// the right shape; a too-new schema must be refused regardless of content.
	header := Header{FormatVersion: formatVersion, SchemaVersion: database.SchemaVersion + 1}
	var archive bytes.Buffer
	if err := encryptArchive(&archive, header, []byte("SQLite format 3\x00"), "pass"); err != nil {
		t.Fatalf("encryptArchive: %v", err)
	}

	err := Restore(context.Background(), filepath.Join(dir, "out.db"), "pass", &archive, RestoreOptions{})
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
}

func TestRestoreRejectsForeignFile(t *testing.T) {
	dir := t.TempDir()
	err := Restore(context.Background(), filepath.Join(dir, "out.db"), "pass",
		bytes.NewReader([]byte("not a backup at all")), RestoreOptions{})
	if !errors.Is(err, ErrBadFormat) {
		t.Fatalf("err = %v, want ErrBadFormat", err)
	}
}
