package repository

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
)

// CredentialRepository handles credential database operations.
type CredentialRepository struct {
	db            *database.DB
	encryptionKey []byte
}

// NewCredentialRepository creates a new credential repository.
func NewCredentialRepository(db *database.DB, encryptionKey []byte) *CredentialRepository {
	return &CredentialRepository{
		db:            db,
		encryptionKey: encryptionKey,
	}
}

// encrypt encrypts data using AES-GCM.
func (r *CredentialRepository) encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(r.encryptionKey)
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

// decrypt decrypts data using AES-GCM.
func (r *CredentialRepository) decrypt(ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(r.encryptionKey)
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
	query := `SELECT encrypted_data FROM credentials WHERE id = ?`

	var encryptedData string
	err := r.db.DB().QueryRowContext(ctx, query, id).Scan(&encryptedData)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("credential not found")
		}
		return "", err
	}

	// Update last_used timestamp (best-effort, non-critical)
	updateQuery := `UPDATE credentials SET last_used = ? WHERE id = ?`
	//nolint:errcheck // best-effort timestamp update, non-critical
	r.db.DB().ExecContext(ctx, updateQuery, time.Now(), id)

	return r.decrypt(encryptedData)
}

// Create creates a new credential.
func (r *CredentialRepository) Create(ctx context.Context, req models.CredentialCreateRequest) (*models.Credential, error) {
	encryptedData, err := r.encrypt(req.Secret)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	now := time.Now()

	query := `INSERT INTO credentials (id, name, type, description, encrypted_data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err = r.db.DB().ExecContext(ctx, query, id, req.Name, req.Type, req.Description, encryptedData, now, now)
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

// Update updates a credential.
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

	if req.Secret != nil && *req.Secret != "" {
		encryptedData, err := r.encrypt(*req.Secret)
		if err != nil {
			return nil, err
		}
		query := `UPDATE credentials SET name = ?, description = ?, encrypted_data = ?, updated_at = ? WHERE id = ?`
		_, err = r.db.DB().ExecContext(ctx, query, existing.Name, existing.Description, encryptedData, now, id)
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
