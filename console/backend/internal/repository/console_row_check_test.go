package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// consoleRowCheckMarker precedes the SQL block in docs/security-controls.md
// that operators run before upgrading.
const consoleRowCheckMarker = "<!-- console-row-check:"

// documentedConsoleRowCheck returns the SQL exactly as the docs print it, so
// the query operators copy is the query this test runs.
func documentedConsoleRowCheck(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile("../../../../docs/security-controls.md")
	require.NoError(t, err)
	_, after, found := strings.Cut(string(doc), consoleRowCheckMarker)
	require.True(t, found, "the docs must keep the console-row-check marker")
	_, block, found := strings.Cut(after, "```sql\n")
	require.True(t, found, "the marker must be followed by a sql block")
	query, _, found := strings.Cut(block, "```")
	require.True(t, found)
	return query
}

// TestConsoleRowCheckQueryFlagsPlantedRows runs the documented pre-upgrade
// check over a store holding the console's genuine rows, an operator row, and
// every kind of row the credentials API used to accept in the console's
// namespace. Each row must come back with exactly the flags the docs promise.
func TestConsoleRowCheckQueryFlagsPlantedRows(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	kr, err := keyring.NewSingle([]byte(oldKey))
	require.NoError(t, err)
	repo := NewCredentialRepository(db, kr)

	const qube = "0b7c4d2e-1111-4222-8333-444455556666"
	now := time.Now()
	_, err = db.DB().ExecContext(ctx,
		`INSERT INTO qubes (id, name, type, created_at, updated_at) VALUES (?, 'work', 'app', ?, ?)`, qube, now, now)
	require.NoError(t, err)

	want := map[string]string{} // id -> flags; absent means "must not be listed"
	add := func(name, typ, flags string) string {
		c, err := repo.Create(ctx, models.CredentialCreateRequest{Name: name, Type: typ, SecretValue: "x"})
		require.NoError(t, err)
		want[c.ID] = flags
		return c.ID
	}

	// The console's own rows.
	add("qubes-air-ca-cert", "pki", "DUPLICATE")
	add("qubes-air-ca-key", "pki", "DUPLICATE")
	add("qubes-air-luks-master", "pki", "DUPLICATE")
	add("qubes-air-luks-key-"+qube, "pki", "")
	add("qubes-air-luks-legacy-slot-"+qube, "pki", "")
	operator := add("pve-prod", "proxmox", "")
	delete(want, operator)

	// Rows the console never writes.
	add("Qubes-Air-CA-Cert", "pki", "NOT-CANONICAL DUPLICATE")
	add("qubes-air-ca-key", "pki", "DUPLICATE")
	add("qubeſ-air-ca-cert", "pki", "NOT-CANONICAL NON-ASCII")
	add("qubes-air-ca-Key", "pki", "NOT-CANONICAL NON-ASCII")
	add(" qubes-air-luks-master", "pki", "NOT-CANONICAL DUPLICATE")
	add("qubes-air-luks-key-no-such-qube", "pki", "NOT-CANONICAL")
	add("qubes-air-old-token", "proxmox", "NOT-CANONICAL")
	add("legacy-row", "PKI", "NOT-CANONICAL")
	add("zone-b-töken", "api_key", "NOT-CANONICAL NON-ASCII")

	rows, err := db.DB().QueryContext(ctx, documentedConsoleRowCheck(t))
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, name, typ, flags string
		var created any
		require.NoError(t, rows.Scan(&id, &name, &typ, &created, &flags))
		got[id] = flags
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, want, got)
}
