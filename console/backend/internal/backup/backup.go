// Package backup creates and restores encrypted copies of the console database.
//
// The console is a single failure domain: the SQLite database holds the qube
// inventory, the provider identities that replace a terraform state file, the
// certificate registry, and the credential store — and those credentials (the
// CA key among them) are encrypted with a keyring key that lives OUTSIDE the
// database. This package therefore protects the archive with a passphrase that
// is independent of that key, so a stolen backup file is useless on its own,
// and documents in Restore what the operator must supply in addition.
//
// The snapshot is taken with SQLite's VACUUM INTO, which yields a consistent,
// compact copy of a live database (including anything still in the WAL) without
// stopping the console.
package backup

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/slchris/qubes-air/console/internal/database"
)

const (
	// magic identifies the container and its format generation.
	magic = "QUBESAIR-BACKUP"
	// formatVersion is the container layout, bumped only on an incompatible
	// change to the framing or crypto below.
	formatVersion = 1
	// saltLen and nonceLen are the AES-GCM/scrypt parameters on disk.
	saltLen  = 16
	nonceLen = 12
	// scryptCost is deliberately on the slow side: the passphrase is the only
	// thing protecting a file that contains the encrypted credential store.
	scryptCost = 1 << 15
)

// Errors callers switch on.
var (
	ErrBadFormat     = errors.New("backup: not a Qubes Air backup or unsupported format version")
	ErrBadPassphrase = errors.New("backup: wrong passphrase or corrupted archive")
	ErrSchemaTooNew  = errors.New("backup: schema is newer than this console supports")
	ErrTargetExists  = errors.New("backup: restore target exists (use force to overwrite)")
	ErrNotSQLite     = errors.New("backup: decrypted payload is not a SQLite database")
)

// Header describes an archive. It is written in cleartext (it carries no
// secrets) so restore can refuse an incompatible archive before doing scrypt.
type Header struct {
	FormatVersion int       `json:"format_version"`
	SchemaVersion int       `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
	BuildVersion  string    `json:"build_version,omitempty"`
}

// RestoreOptions controls how a restore treats the target.
type RestoreOptions struct {
	// Force permits overwriting an existing database file. A restore replaces
	// the live database, so this is the explicit confirmation the operator
	// gives for that destructive step.
	Force bool
}

// Create snapshots dbPath and writes an encrypted archive to w.
func Create(ctx context.Context, dbPath, passphrase, buildVersion string, w io.Writer) error {
	if passphrase == "" {
		return errors.New("backup: passphrase must not be empty")
	}

	tmpDir, err := os.MkdirTemp("", "qubesair-backup-*")
	if err != nil {
		return fmt.Errorf("backup: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	snapshot := filepath.Join(tmpDir, "snapshot.db")

	src, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("backup: open database: %w", err)
	}
	defer src.Close()

	// VACUUM INTO writes a consistent snapshot of the live database, WAL included.
	if _, err := src.ExecContext(ctx, "VACUUM INTO ?", snapshot); err != nil {
		return fmt.Errorf("backup: snapshot database: %w", err)
	}

	payload, err := os.ReadFile(snapshot)
	if err != nil {
		return fmt.Errorf("backup: read snapshot: %w", err)
	}
	if !isSQLite(payload) {
		return ErrNotSQLite
	}
	schemaVersion, err := readUserVersion(snapshot)
	if err != nil {
		return err
	}

	header := Header{
		FormatVersion: formatVersion,
		SchemaVersion: schemaVersion,
		CreatedAt:     time.Now().UTC(),
		BuildVersion:  buildVersion,
	}
	return encryptArchive(w, header, payload, passphrase)
}

// Restore decrypts r and writes the database to dbPath.
//
// The operator must ALSO have the console's keyring key (security.encryption_key
// / encryption_keys, or QUBES_AIR_ENCRYPTION_KEYS): the credentials and CA key
// in the restored database stay encrypted after this call and cannot be read
// without it. That key is intentionally not part of the archive.
func Restore(_ context.Context, dbPath, passphrase string, r io.Reader, opts RestoreOptions) error {
	if passphrase == "" {
		return errors.New("backup: passphrase must not be empty")
	}
	header, headerJSON, ciphertext, err := readArchive(r)
	if err != nil {
		return err
	}
	if header.FormatVersion != formatVersion {
		return fmt.Errorf("%w: archive format %d, this build reads %d", ErrBadFormat, header.FormatVersion, formatVersion)
	}
	if header.SchemaVersion > database.SchemaVersion {
		return fmt.Errorf("%w: archive schema %d, this console supports %d",
			ErrSchemaTooNew, header.SchemaVersion, database.SchemaVersion)
	}

	payload, err := decryptArchive(headerJSON, ciphertext, passphrase)
	if err != nil {
		return err
	}
	if !isSQLite(payload) {
		return ErrNotSQLite
	}
	return installDatabase(dbPath, payload, opts.Force)
}

// installDatabase writes payload beside dbPath and atomically replaces the
// target, removing stale WAL/SHM sidecars first. Split out of Restore so the
// validation and the file installation are not one long function.
func installDatabase(dbPath string, payload []byte, force bool) error {
	if _, statErr := os.Stat(dbPath); statErr == nil && !force {
		return fmt.Errorf("%w: %s", ErrTargetExists, dbPath)
	}

	// Write beside the target so the final rename is atomic on one filesystem.
	dir := filepath.Dir(dbPath)
	tmp, err := os.CreateTemp(dir, ".restore-*.db")
	if err != nil {
		return fmt.Errorf("backup: create temp restore file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("backup: write restore file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("backup: sync restore file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("backup: close restore file: %w", err)
	}

	// A stale WAL beside the replaced database would be applied to the restored
	// file and corrupt it, so the sidecars go with the old file.
	for _, sidecar := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("backup: remove stale %s: %w", filepath.Base(sidecar), err)
		}
	}
	if err := os.Rename(tmpName, dbPath); err != nil {
		return fmt.Errorf("backup: replace database: %w", err)
	}
	return nil
}

// encryptArchive frames and encrypts payload:
//
//	magic \n  header-json \n  salt || nonce || AES-256-GCM(payload)
func encryptArchive(w io.Writer, header Header, payload []byte, passphrase string) error {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("backup: encode header: %w", err)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("backup: salt: %w", err)
	}
	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("backup: nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, payload, headerJSON)

	if _, err := fmt.Fprintf(w, "%s\n%s\n", magic, headerJSON); err != nil {
		return err
	}
	if _, err := w.Write(salt); err != nil {
		return err
	}
	if _, err := w.Write(nonce); err != nil {
		return err
	}
	if _, err := w.Write(sealed); err != nil {
		return err
	}
	return nil
}

// readArchive parses the cleartext frame and returns the header, its exact
// bytes (needed as AES-GCM additional data), and the salt||nonce||ciphertext
// tail. It does no crypto, so a foreign file is rejected cheaply.
func readArchive(r io.Reader) (Header, []byte, []byte, error) {
	br := bufio.NewReader(r)
	gotMagic, err := br.ReadString('\n')
	if err != nil {
		return Header{}, nil, nil, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	if gotMagic != magic+"\n" {
		return Header{}, nil, nil, ErrBadFormat
	}
	headerLine, err := br.ReadString('\n')
	if err != nil {
		return Header{}, nil, nil, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	var header Header
	if err := json.Unmarshal([]byte(headerLine), &header); err != nil {
		return Header{}, nil, nil, fmt.Errorf("%w: header: %v", ErrBadFormat, err)
	}
	headerJSON := []byte(strings.TrimRight(headerLine, "\n"))
	tail, err := io.ReadAll(br)
	if err != nil {
		return Header{}, nil, nil, fmt.Errorf("%w: %v", ErrBadFormat, err)
	}
	if len(tail) < saltLen+nonceLen+gcmTagLen {
		return Header{}, nil, nil, ErrBadFormat
	}
	return header, headerJSON, tail, nil
}

// gcmTagLen is the AES-GCM authentication tag length.
const gcmTagLen = 16

func decryptArchive(headerJSON, tail []byte, passphrase string) ([]byte, error) {
	salt := tail[:saltLen]
	nonce := tail[saltLen : saltLen+nonceLen]
	ciphertext := tail[saltLen+nonceLen:]
	key, err := deriveKey(passphrase, salt)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	payload, err := gcm.Open(nil, nonce, ciphertext, headerJSON)
	if err != nil {
		return nil, ErrBadPassphrase
	}
	return payload, nil
}

func deriveKey(passphrase string, salt []byte) ([]byte, error) {
	key, err := scrypt.Key([]byte(passphrase), salt, scryptCost, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("backup: derive key: %w", err)
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("backup: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("backup: gcm: %w", err)
	}
	return gcm, nil
}

// isSQLite reports whether b begins with the SQLite file header.
func isSQLite(b []byte) bool {
	const header = "SQLite format 3\x00"
	return len(b) >= len(header) && string(b[:len(header)]) == header
}

// readUserVersion returns PRAGMA user_version from a database file.
func readUserVersion(path string) (int, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return 0, fmt.Errorf("backup: open snapshot: %w", err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("backup: read schema version: %w", err)
	}
	return v, nil
}
