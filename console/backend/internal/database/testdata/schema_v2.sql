-- Schema-version-2 database, as a console built from 2d409fd creates it.
--
-- The DDL below is sqlite_master.sql dumped from a fresh database that build
-- opened (the ALTER-added qubes columns appear inline because SQLite rewrites
-- the stored CREATE TABLE text on ADD COLUMN). The rows after it are what a
-- running v2 fleet holds and what every later schema step must carry over:
-- an unredeemed and a redeemed bootstrap token, a disconnected zone, one
-- healthy and one unreachable qube, and notification settings that still
-- keep their webhook URL in plaintext.
--
-- Loaded by openV2Fixture (database_upgrade_test.go), which stamps
-- user_version = 2. Do not regenerate this from a newer build: its value is
-- that it is frozen at v2.
CREATE TABLE zones (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'disconnected',
	config TEXT DEFAULT '{}',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE TABLE qubes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	zone_id TEXT DEFAULT '',
	status TEXT NOT NULL DEFAULT 'stopped',
	spec TEXT DEFAULT '{}',
	ip_address TEXT DEFAULT '',
	agent_health TEXT NOT NULL DEFAULT 'unknown',
	agent_last_probed_at DATETIME,
	agent_last_healthy_at DATETIME,
	agent_last_error TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
, purge_requested INTEGER NOT NULL DEFAULT 0, agent_failing_since DATETIME);
CREATE TABLE infrastructure (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'disconnected',
	region TEXT DEFAULT '',
	config TEXT DEFAULT '{}',
	resource_count INTEGER DEFAULT 0,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE TABLE credentials (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	description TEXT DEFAULT '',
	encrypted_data TEXT NOT NULL,
	key_version INTEGER NOT NULL DEFAULT 1,
	last_used DATETIME,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE TABLE settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE TABLE jobs (
	id TEXT PRIMARY KEY,
	qube_id TEXT NOT NULL,
	qube_name TEXT NOT NULL,
	action TEXT NOT NULL,
	state TEXT NOT NULL,
	error TEXT DEFAULT '',
	enqueued_at DATETIME NOT NULL,
	started_at DATETIME,
	finished_at DATETIME
);
CREATE INDEX idx_jobs_qube_id ON jobs(qube_id);
CREATE INDEX idx_jobs_enqueued_at ON jobs(enqueued_at DESC);
CREATE INDEX idx_jobs_state ON jobs(state);
CREATE TABLE agent_certs (
	fingerprint TEXT PRIMARY KEY,
	qube_id     TEXT NOT NULL,
	subject_cn  TEXT NOT NULL,
	issued_at   DATETIME NOT NULL,
	expires_at  DATETIME,
	revoked_at  DATETIME,
	revoked_reason TEXT DEFAULT '',
	last_seen_at   DATETIME
);
CREATE INDEX idx_agent_certs_qube_id ON agent_certs(qube_id);
CREATE INDEX idx_agent_certs_revoked ON agent_certs(revoked_at);
CREATE TABLE bootstrap_tokens (
	secret_hash TEXT PRIMARY KEY,
	qube_id     TEXT NOT NULL,
	qube_name   TEXT NOT NULL,
	created_at  DATETIME NOT NULL,
	not_after   DATETIME NOT NULL,
	redeemed_at DATETIME
);
CREATE INDEX idx_bootstrap_tokens_qube_id ON bootstrap_tokens(qube_id);
CREATE INDEX idx_bootstrap_tokens_not_after ON bootstrap_tokens(not_after);
CREATE TABLE qube_infra (
	qube_id        TEXT PRIMARY KEY,
	provider       TEXT NOT NULL DEFAULT '',
	node           TEXT NOT NULL DEFAULT '',
	storage_vmid   INTEGER NOT NULL DEFAULT 0,
	compute_vmid   INTEGER NOT NULL DEFAULT 0,
	data_volume    TEXT NOT NULL DEFAULT '',
	identity_vol   TEXT NOT NULL DEFAULT '',
	observed_state TEXT NOT NULL DEFAULT '',
	protected      INTEGER NOT NULL DEFAULT 1,
	observed_at    DATETIME,
	created_at     DATETIME NOT NULL,
	updated_at     DATETIME NOT NULL
);
CREATE TRIGGER purge_withdraw_identity
AFTER UPDATE OF purge_requested ON qubes
WHEN NEW.purge_requested = 1 AND OLD.purge_requested = 0
BEGIN
 UPDATE bootstrap_tokens SET redeemed_at = NEW.updated_at
 WHERE qube_id = NEW.id AND redeemed_at IS NULL;
 UPDATE agent_certs SET revoked_at = NEW.updated_at, revoked_reason = 'purge'
 WHERE qube_id = NEW.id AND revoked_at IS NULL;
END;
CREATE TRIGGER purge_reject_certificate
BEFORE INSERT ON agent_certs
WHEN EXISTS (SELECT 1 FROM qubes WHERE id = NEW.qube_id AND purge_requested = 1)
BEGIN SELECT RAISE(ABORT, 'purge requested: certificate issuance forbidden'); END;
CREATE TRIGGER purge_reject_bootstrap
BEFORE INSERT ON bootstrap_tokens
WHEN EXISTS (SELECT 1 FROM qubes WHERE id = NEW.qube_id AND purge_requested = 1)
BEGIN SELECT RAISE(ABORT, 'purge requested: bootstrap issuance forbidden'); END;

INSERT INTO zones (id, name, type, status, config, created_at, updated_at)
VALUES ('zone-pve-lab', 'pve-lab', 'proxmox', 'disconnected', '{}',
        '2026-09-20 09:00:00+00:00', '2026-09-20 09:00:00+00:00');

INSERT INTO qubes (id, name, type, zone_id, status, spec, ip_address,
                   agent_health, agent_last_probed_at, agent_last_healthy_at,
                   agent_last_error, created_at, updated_at,
                   purge_requested, agent_failing_since)
VALUES ('qube-healthy', 'remote-healthy', 'app', 'zone-pve-lab', 'running', '{}',
        '192.0.2.10', 'healthy', '2026-09-20 09:55:00+00:00',
        '2026-09-20 09:55:00+00:00', '', '2026-09-20 09:10:00+00:00',
        '2026-09-20 09:55:00+00:00', 0, NULL);

INSERT INTO qubes (id, name, type, zone_id, status, spec, ip_address,
                   agent_health, agent_last_probed_at, agent_last_healthy_at,
                   agent_last_error, created_at, updated_at,
                   purge_requested, agent_failing_since)
VALUES ('qube-pending', 'remote-pending', 'dev', 'zone-pve-lab', 'running', '{}',
        '192.0.2.11', 'unreachable', '2026-09-20 09:58:00+00:00', NULL,
        'dial tcp 192.0.2.11:8443: connect: connection refused',
        '2026-09-20 09:50:00+00:00', '2026-09-20 09:58:00+00:00', 0,
        '2026-09-20 09:52:00+00:00');

-- Redeemed: the healthy qube spent its token when it bootstrapped.
INSERT INTO bootstrap_tokens (secret_hash, qube_id, qube_name, created_at,
                              not_after, redeemed_at)
VALUES ('1111111111111111111111111111111111111111111111111111111111111111',
        'qube-healthy', 'remote-healthy', '2026-09-20 09:10:00+00:00',
        '2026-09-20 10:10:00+00:00', '2026-09-20 09:20:00+00:00');

-- Unredeemed and inside its one-hour TTL at 2026-09-20 10:00Z: a qube that was
-- provisioned but had not bootstrapped yet when the console was upgraded.
INSERT INTO bootstrap_tokens (secret_hash, qube_id, qube_name, created_at,
                              not_after, redeemed_at)
VALUES ('2222222222222222222222222222222222222222222222222222222222222222',
        'qube-pending', 'remote-pending', '2026-09-20 09:50:00+00:00',
        '2026-09-20 10:50:00+00:00', NULL);

INSERT INTO agent_certs (fingerprint, qube_id, subject_cn, issued_at,
                         expires_at, revoked_at, revoked_reason, last_seen_at)
VALUES ('3333333333333333333333333333333333333333333333333333333333333333',
        'qube-healthy', 'agent-remote-healthy', '2026-09-20 09:20:00+00:00',
        '2026-12-19 09:20:00+00:00', NULL, '', '2026-09-20 09:55:00+00:00');

INSERT INTO settings (key, value, updated_at)
VALUES ('notifications',
        '{"email":false,"webhook":true,"webhookUrl":"https://hooks.example.invalid/qubes-air"}',
        '2026-09-20 09:00:00+00:00');
