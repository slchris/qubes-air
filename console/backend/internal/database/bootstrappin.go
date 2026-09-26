package database

// migrateBootstrapPeerPin is schema step 3. It adds
// bootstrap_tokens.placeholder_spki_sha256: the SHA-256 pin of the public key
// the token derives for the agent's pre-identity listener
// (pki.BootstrapPlaceholderSPKIFingerprint). It is a public-key digest, stored
// so the console can authenticate that listener at first contact without
// keeping the token itself.
//
// Rows from before it exist get an empty pin rather than NULL, and an empty
// pin is deliberately a value the reader refuses: a token minted before pinning
// has no pin to check, and
// repository.BootstrapTokenRepository.PendingPlaceholderSPKIFingerprint reports
// it as "re-provision this qube" instead of dialing it unauthenticated. Such
// tokens live at most an hour, and the agent holding one predates the
// token-derived placeholder anyway, so there is nothing to backfill.
func (d *DB) migrateBootstrapPeerPin() error {
	return d.addColumnIfMissing("bootstrap_tokens", "placeholder_spki_sha256", "TEXT NOT NULL DEFAULT ''")
}
