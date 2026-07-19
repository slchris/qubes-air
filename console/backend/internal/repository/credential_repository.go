package repository

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
)

// CredentialRepository handles credential database operations.
//
// Encryption uses AES-256-GCM. Each row records the key_version that encrypted
// its ciphertext; the repository holds a keyring of one or more versioned keys
// so the encryption key can be rotated (see RotateStats / cmd/rotate-key)
// without invalidating existing ciphertext.
type CredentialRepository struct {
	db      *database.DB
	keyring *keyring.Keyring
}

// NewCredentialRepository creates a new credential repository backed by a
// keyring. New credentials are encrypted with the keyring's primary version;
// existing credentials are decrypted with the key matching their stored
// key_version.
func NewCredentialRepository(db *database.DB, kr *keyring.Keyring) *CredentialRepository {
	return &CredentialRepository{
		db:      db,
		keyring: kr,
	}
}

// encryptWith encrypts plaintext with the key for the given version.
func (r *CredentialRepository) encryptWith(plaintext string, version int) (string, error) {
	key, err := r.keyring.Key(version)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptWith decrypts ciphertext using the key for the given version.
func (r *CredentialRepository) decryptWith(ciphertext string, version int) (string, error) {
	key, err := r.keyring.Key(version)
	if err != nil {
		return "", err
	}

	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// List returns all credentials (without secrets).
func (r *CredentialRepository) List(ctx context.Context) ([]models.Credential, error) {
	query := `SELECT id, name, type, description, created_at, updated_at, last_used FROM credentials ORDER BY created_at DESC`

	rows, err := r.db.DB().QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var credentials []models.Credential
	for rows.Next() {
		var c models.Credential
		var lastUsed sql.NullTime
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Description, &c.CreatedAt, &c.UpdatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			c.LastUsed = &lastUsed.Time
		}
		credentials = append(credentials, c)
	}

	return credentials, rows.Err()
}

// GetByID returns a credential by ID (without secret).
func (r *CredentialRepository) GetByID(ctx context.Context, id string) (*models.Credential, error) {
	query := `SELECT id, name, type, description, created_at, updated_at, last_used FROM credentials WHERE id = ?`

	var c models.Credential
	var lastUsed sql.NullTime
	err := r.db.DB().QueryRowContext(ctx, query, id).Scan(
		&c.ID, &c.Name, &c.Type, &c.Description, &c.CreatedAt, &c.UpdatedAt, &lastUsed,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if lastUsed.Valid {
		c.LastUsed = &lastUsed.Time
	}

	return &c, nil
}

// GetSecret returns the decrypted secret for a credential.
func (r *CredentialRepository) GetSecret(ctx context.Context, id string) (string, error) {
	query := `SELECT encrypted_data, key_version FROM credentials WHERE id = ?`

	var (
		encryptedData string
		keyVersion    int
	)
	err := r.db.DB().QueryRowContext(ctx, query, id).Scan(&encryptedData, &keyVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("credential not found")
		}
		return "", err
	}

	// Update last_used timestamp (best-effort, non-critical)
	updateQuery := `UPDATE credentials SET last_used = ? WHERE id = ?`
	if _, execErr := r.db.DB().ExecContext(ctx, updateQuery, time.Now(), id); execErr != nil {
		// Non-critical: log internally but don't fail the operation
		_ = execErr
	}

	return r.decryptWith(encryptedData, keyVersion)
}

// Create creates a new credential, encrypting the secret with the keyring's
// primary (current) key version and recording that version on the row.
func (r *CredentialRepository) Create(ctx context.Context, req models.CredentialCreateRequest) (*models.Credential, error) {
	version := r.keyring.PrimaryVersion()
	encryptedData, err := r.encryptWith(req.SecretValue, version)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	now := time.Now()

	query := `INSERT INTO credentials (id, name, type, description, encrypted_data, key_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = r.db.DB().ExecContext(ctx, query, id, req.Name, req.Type, req.Description, encryptedData, version, now, now)
	if err != nil {
		return nil, err
	}

	return &models.Credential{
		ID:          id,
		Name:        req.Name,
		Type:        req.Type,
		Description: req.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// Update updates a credential. When the secret is changed it is re-encrypted
// with the keyring's primary version and key_version is updated accordingly.
func (r *CredentialRepository) Update(ctx context.Context, id string, req models.CredentialUpdateRequest) (*models.Credential, error) {
	// Get existing credential
	existing, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, errors.New("credential not found")
	}

	now := time.Now()

	// Apply updates
	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}

	if req.SecretValue != nil && *req.SecretValue != "" {
		version := r.keyring.PrimaryVersion()
		encryptedData, err := r.encryptWith(*req.SecretValue, version)
		if err != nil {
			return nil, err
		}
		query := `UPDATE credentials SET name = ?, description = ?, encrypted_data = ?, key_version = ?, updated_at = ? WHERE id = ?`
		_, err = r.db.DB().ExecContext(ctx, query, existing.Name, existing.Description, encryptedData, version, now, id)
		if err != nil {
			return nil, err
		}
	} else {
		query := `UPDATE credentials SET name = ?, description = ?, updated_at = ? WHERE id = ?`
		_, err := r.db.DB().ExecContext(ctx, query, existing.Name, existing.Description, now, id)
		if err != nil {
			return nil, err
		}
	}

	existing.UpdatedAt = now
	return existing, nil
}

// Delete deletes a credential.
func (r *CredentialRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM credentials WHERE id = ?`
	result, err := r.db.DB().ExecContext(ctx, query, id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errors.New("credential not found")
	}

	return nil
}

// RotateStats reports the outcome of a key rotation pass.
type RotateStats struct {
	// Total is the number of credential rows examined.
	Total int
	// Reencrypted is the number of rows re-encrypted to the target version.
	Reencrypted int
	// AlreadyCurrent is the number of rows already at the target version
	// (left untouched, making rotation resumable/idempotent).
	AlreadyCurrent int
	// TargetVersion is the key version rows were rotated to.
	TargetVersion int
}

// RotateToPrimary re-encrypts every credential whose key_version is not the
// keyring's primary version, using the primary key, inside a single database
// transaction.
//
// Atomicity: all rows are decrypted-with-old-key and re-encrypted-with-new-key
// within one transaction. If ANY row fails (e.g. a key version is missing from
// the keyring, or ciphertext is corrupt), the whole transaction is rolled back
// and the database is left exactly as before — there is never a half-rotated
// state. Rows already at the primary version are skipped, so a re-run after a
// partial/aborted attempt simply finishes the job (idempotent).
//
// Precondition: the keyring must contain the key for EVERY key_version present
// in the table, otherwise decryption of those rows fails and the whole rotation
// aborts (fail-closed).
func (r *CredentialRepository) RotateToPrimary(ctx context.Context) (RotateStats, error) {
	target := r.keyring.PrimaryVersion()
	stats := RotateStats{TargetVersion: target}

	tx, err := r.db.DB().BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("begin tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit.
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id, encrypted_data, key_version FROM credentials`)
	if err != nil {
		return stats, fmt.Errorf("select credentials: %w", err)
	}

	type rowData struct {
		id         string
		ciphertext string
		version    int
	}
	var all []rowData
	for rows.Next() {
		var rd rowData
		if err := rows.Scan(&rd.id, &rd.ciphertext, &rd.version); err != nil {
			_ = rows.Close()
			return stats, fmt.Errorf("scan credential: %w", err)
		}
		all = append(all, rd)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return stats, err
	}
	if err := rows.Close(); err != nil {
		return stats, err
	}

	for _, rd := range all {
		stats.Total++
		if rd.version == target {
			stats.AlreadyCurrent++
			continue
		}

		plaintext, err := r.decryptWith(rd.ciphertext, rd.version)
		if err != nil {
			return stats, fmt.Errorf("decrypt credential %s (key_version=%d): %w", rd.id, rd.version, err)
		}
		newCipher, err := r.encryptWith(plaintext, target)
		if err != nil {
			return stats, fmt.Errorf("re-encrypt credential %s to version %d: %w", rd.id, target, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE credentials SET encrypted_data = ?, key_version = ? WHERE id = ?`,
			newCipher, target, rd.id,
		); err != nil {
			return stats, fmt.Errorf("update credential %s: %w", rd.id, err)
		}
		stats.Reencrypted++
	}

	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit rotation: %w", err)
	}
	return stats, nil
}
