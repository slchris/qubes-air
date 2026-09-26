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

// Markers precede the SQL blocks in docs/security-controls.md that operators
// run before upgrading.
const (
	consoleRowCheckMarker     = "<!-- console-row-check:"
	consoleZoneRefCheckMarker = "<!-- console-zone-ref-check:"
)

// documentedSQL returns the SQL block after marker exactly as the docs print
// it, so the query operators copy is the query the test runs.
func documentedSQL(t *testing.T, marker string) string {
	t.Helper()
	doc, err := os.ReadFile("../../../../docs/security-controls.md")
	require.NoError(t, err)
	_, after, found := strings.Cut(string(doc), marker)
	require.True(t, found, "the docs must keep the %s marker", marker)
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
	// Types the API hides as the console's because they fold or trim to "pki".
	add("pve-kelvin", "p\u212ai", "NOT-CANONICAL NON-ASCII")
	add("pve-nel", "\u0085pki", "NOT-CANONICAL NON-ASCII")
	add("pve-pad", "pki ", "NOT-CANONICAL")
	add("pve-upper", " PKI ", "NOT-CANONICAL")

	rows, err := db.DB().QueryContext(ctx, documentedSQL(t, consoleRowCheckMarker))
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

	// Whatever the fixture holds, every row the credentials API hides must be
	// listed: the check is how an operator finds rows the API no longer shows.
	all, err := repo.List(ctx)
	require.NoError(t, err)
	for _, c := range all {
		if models.IsConsoleCredential(c.Name, c.Type) {
			assert.Contains(t, got, c.ID, "hidden row %q (type %q) must be listed", c.Name, c.Type)
		}
	}
}

// TestZoneReferenceCheckQueryFlagsHiddenReferences runs the documented zone
// check: every zone whose Proxmox or GCP credential_id names a missing row, a
// row in the console's namespace, a pki-typed row or a non-ASCII-named row is
// listed with the matching flags; zones referencing ordinary operator rows,
// or nothing, are not listed.
func TestZoneReferenceCheckQueryFlagsHiddenReferences(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	kr, err := keyring.NewSingle([]byte(oldKey))
	require.NoError(t, err)
	creds := NewCredentialRepository(db, kr)
	zones := NewZoneRepository(db)

	cred := func(name, typ string) string {
		c, err := creds.Create(ctx, models.CredentialCreateRequest{Name: name, Type: typ, SecretValue: "x"})
		require.NoError(t, err)
		return c.ID
	}
	operator := cred("pve-prod", "proxmox")
	caKey := cred(models.ConsoleCAKeyName, models.ConsoleRowType)
	legacyNamespaced := cred("qubes-air-old-token", "proxmox")
	legacyPKI := cred("legacy-row", "PKI")
	nonASCII := cred("zone-b-t\u00f6ken", "proxmox")
	lookalike := cred("qube\u017f-air-ca-key", "other")
	kelvinType := cred("pve-kelvin", "p\u212ai")
	nelType := cred("pve-nel", "\u0085pki")
	paddedType := cred("pve-pad", "pki ")
	upperPaddedType := cred("pve-upper", " PKI ")
	paddedName := cred("  QUBES-AIR-x", "proxmox")

	want := map[string]string{} // zone id + path -> flags
	zone := func(id string, cfg models.ZoneConfig) {
		now := time.Now()
		require.NoError(t, zones.Create(ctx, &models.Zone{
			ID: id, Name: id, Type: models.ZoneTypeProxmox, Status: models.ZoneStatusConnected,
			Config: cfg, CreatedAt: now, UpdatedAt: now,
		}))
	}
	proxmox := func(id, ref, flags string) {
		zone(id, models.ZoneConfig{Proxmox: &models.ProxmoxZoneConfig{CredentialID: ref}})
		if flags != "-" {
			want[id+" $.proxmox.credential_id"] = flags
		}
	}
	proxmox("z-operator", operator, "-")
	proxmox("z-none", "", "-")
	proxmox("z-ca-key", caKey, "CONSOLE-NAMESPACE PKI-TYPE")
	proxmox("z-legacy-namespaced", legacyNamespaced, "CONSOLE-NAMESPACE")
	proxmox("z-legacy-pki", legacyPKI, "PKI-TYPE")
	proxmox("z-non-ascii", nonASCII, "NON-ASCII")
	proxmox("z-lookalike", lookalike, "NON-ASCII")
	proxmox("z-missing", "no-such-credential", "MISSING")
	proxmox("z-kelvin-type", kelvinType, "NON-ASCII")
	proxmox("z-nel-type", nelType, "NON-ASCII")
	proxmox("z-padded-type", paddedType, "PKI-TYPE")
	proxmox("z-upper-padded-type", upperPaddedType, "PKI-TYPE")
	proxmox("z-padded-name", paddedName, "CONSOLE-NAMESPACE")
	zone("z-gcp", models.ZoneConfig{GCP: &models.GCPZoneConfig{CredentialID: caKey}})
	want["z-gcp $.gcp.credential_id"] = "CONSOLE-NAMESPACE PKI-TYPE"
	zone("z-both", models.ZoneConfig{
		Proxmox: &models.ProxmoxZoneConfig{CredentialID: operator},
		GCP:     &models.GCPZoneConfig{CredentialID: "gone"},
	})
	want["z-both $.gcp.credential_id"] = "MISSING"

	rows, err := db.DB().QueryContext(ctx, documentedSQL(t, consoleZoneRefCheckMarker))
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var zoneID, name, path, ref, flags string
		var credName any
		require.NoError(t, rows.Scan(&zoneID, &name, &path, &ref, &credName, &flags))
		got[zoneID+" "+path] = flags
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, want, got)

	// Whatever the fixture holds, a zone whose reference is missing or names a
	// row the credentials API hides is listed. (The query also lists zones
	// referencing non-ASCII-named operator rows, for a human to judge.)
	all, err := zones.List(ctx, DefaultZoneListOptions())
	require.NoError(t, err)
	for _, z := range all {
		refs := map[string]string{}
		if z.Config.Proxmox != nil {
			refs["$.proxmox.credential_id"] = z.Config.Proxmox.CredentialID
		}
		if z.Config.GCP != nil {
			refs["$.gcp.credential_id"] = z.Config.GCP.CredentialID
		}
		for path, ref := range refs {
			if ref == "" {
				continue
			}
			row, err := creds.GetByID(ctx, ref)
			require.NoError(t, err)
			if row == nil || models.IsConsoleCredential(row.Name, row.Type) {
				assert.Contains(t, got, z.ID+" "+path, "zone %s references a hidden or missing row", z.ID)
			}
		}
	}
}
