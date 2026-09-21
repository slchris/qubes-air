package database

import "context"

// Purge intent and authorization withdrawal share SQLite's write transaction.
// Insert guards close bootstrap/renewal races that started before the claim.
func (d *DB) migrateLifecycle() error {
	_, err := d.db.ExecContext(context.Background(), `
CREATE TRIGGER IF NOT EXISTS purge_withdraw_identity
AFTER UPDATE OF purge_requested ON qubes
WHEN NEW.purge_requested = 1 AND OLD.purge_requested = 0
BEGIN
 UPDATE bootstrap_tokens SET redeemed_at = NEW.updated_at
 WHERE qube_id = NEW.id AND redeemed_at IS NULL;
 UPDATE agent_certs SET revoked_at = NEW.updated_at, revoked_reason = 'purge'
 WHERE qube_id = NEW.id AND revoked_at IS NULL;
END;
CREATE TRIGGER IF NOT EXISTS purge_reject_certificate
BEFORE INSERT ON agent_certs
WHEN EXISTS (SELECT 1 FROM qubes WHERE id = NEW.qube_id AND purge_requested = 1)
BEGIN SELECT RAISE(ABORT, 'purge requested: certificate issuance forbidden'); END;
CREATE TRIGGER IF NOT EXISTS purge_reject_bootstrap
BEFORE INSERT ON bootstrap_tokens
WHEN EXISTS (SELECT 1 FROM qubes WHERE id = NEW.qube_id AND purge_requested = 1)
BEGIN SELECT RAISE(ABORT, 'purge requested: bootstrap issuance forbidden'); END;
`)
	return err
}
