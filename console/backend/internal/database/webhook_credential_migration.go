package database

import (
	"context"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/models"
)

// legacyAlertWebhookTargetCredentialID is the row the webhook code wrote before
// the destination was moved into the console credential namespace. Its name
// and type made CredentialService treat it as an operator row, so a control
// token could list it, read its metadata, and delete it.
const legacyAlertWebhookTargetCredentialID = "system:alert-webhook-target"

// migrateAlertWebhookCredential moves that legacy row to the console-owned
// name and type. The encrypted payload and key_version are preserved, so an
// existing destination keeps working. The operation is idempotent; if both
// rows exist, the new one wins and the old one is removed.
func (d *DB) migrateAlertWebhookCredential() error {
	_, err := d.db.ExecContext(context.Background(), `
		INSERT INTO credentials (id, name, type, description, encrypted_data, key_version, created_at, updated_at)
		SELECT ?, ?, ?, description, encrypted_data, key_version, created_at, updated_at
		FROM credentials WHERE id = ?
		ON CONFLICT(id) DO NOTHING;
		DELETE FROM credentials WHERE id = ?`,
		models.ConsoleWebhookTargetID, models.ConsoleWebhookTargetName, models.ConsoleRowType,
		legacyAlertWebhookTargetCredentialID, legacyAlertWebhookTargetCredentialID)
	if err != nil {
		return fmt.Errorf("migrate alert webhook credential: %w", err)
	}
	return nil
}
