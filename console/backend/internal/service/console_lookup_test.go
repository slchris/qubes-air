package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plantedRow is a row written straight into the store, as the credentials API
// allowed before it reserved the console's namespace.
type plantedRow struct{ name, typ string }

// lookalikes are the rows a caller could have planted under a console name:
// each one answers to the name under the console's comparison, and none of
// them is the one exact, pki-typed row the console writes.
func lookalikes(name, withKelvin string) map[string]plantedRow {
	cases := map[string]plantedRow{
		"case variant":         {caseVariant(name), models.ConsoleRowType},
		"long s variant":       {longSVariant(name), models.ConsoleRowType},
		"padded":               {" " + name, models.ConsoleRowType},
		"exact-name duplicate": {name, models.ConsoleRowType},
		"wrong type":           {name, "other"},
	}
	if withKelvin != "" {
		cases["Kelvin sign variant"] = plantedRow{withKelvin, models.ConsoleRowType}
	}
	return cases
}

// caseVariant upper-cases the prefix: "QUBES-AIR-ca-key".
func caseVariant(name string) string {
	return "QUBES-AIR-" + name[len(models.ConsoleRowNamePrefix):]
}

// longSVariant spells the "s" of "qubes" as U+017F, which folds to "s".
func longSVariant(name string) string {
	return "qubeſ" + name[len("qubes"):]
}

func newLookupStore(t *testing.T) (*repository.CredentialRepository, *repository.AgentCertRepository) {
	t.Helper()
	db := certTestDB(t)
	kr, err := keyring.NewSingle([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	return repository.NewCredentialRepository(db, kr), repository.NewAgentCertRepository(db)
}

func plant(t *testing.T, repo *repository.CredentialRepository, row plantedRow, secret string) string {
	t.Helper()
	c, err := repo.Create(context.Background(), models.CredentialCreateRequest{Name: row.name, Type: row.typ, SecretValue: secret})
	require.NoError(t, err)
	return c.ID
}

func rowCount(t *testing.T, repo *repository.CredentialRepository) int {
	t.Helper()
	all, err := repo.List(context.Background())
	require.NoError(t, err)
	return len(all)
}

// assertConflict checks the refusal names the rows involved and carries no
// secret, and that it was logged.
func assertConflict(t *testing.T, err error, logged string, ids []string, secrets ...string) {
	t.Helper()
	require.ErrorIs(t, err, models.ErrConsoleRowConflict)
	for _, id := range ids {
		assert.Contains(t, err.Error(), id, "the refusal must name every conflicting row")
		assert.Contains(t, logged, id, "the refusal must be logged with the row IDs")
	}
	assert.Contains(t, logged, "SECURITY")
	for _, s := range secrets {
		assert.NotContains(t, err.Error(), s, "a refusal must not carry secret material")
		assert.NotContains(t, logged, s, "a refusal must not log secret material")
	}
}

// TestCAFailsClosedOnLookalikeRows — a CA row the console did not write makes
// CA() fail, whether the genuine CA already exists (a restarted console must
// not load the planted one) or not yet (the console must not mint a CA next to
// it). The attacker's key material never comes back.
func TestCAFailsClosedOnLookalikeRows(t *testing.T) {
	attacker, err := pki.NewCA("attacker", time.Hour)
	require.NoError(t, err)
	certPEM, keyPEM, err := attacker.MarshalCA()
	require.NoError(t, err)
	ctx := context.Background()

	for label, row := range lookalikes(caKeyCredentialName, "qubes-air-ca-Key") {
		t.Run(label+"/after the CA exists", func(t *testing.T) {
			logged := captureLog(t)
			repo, certs := newLookupStore(t)
			genuine, err := NewCertIssuer(repo, certs, "", "", AgentPackage{}).CA(ctx)
			require.NoError(t, err)
			var realKeyID string
			all, err := repo.List(ctx)
			require.NoError(t, err)
			for _, c := range all {
				if c.Name == caKeyCredentialName {
					realKeyID = c.ID
				}
			}
			plantedID := plant(t, repo, row, keyPEM)

			ca, err := NewCertIssuer(repo, certs, "", "", AgentPackage{}).CA(ctx)
			assert.Nil(t, ca)
			assertConflict(t, err, logged.String(), []string{realKeyID, plantedID}, keyPEM)
			assert.NotNil(t, genuine)
		})
		if label == "exact-name duplicate" {
			continue // alone, it is the console's own row; see TestExactRowPlantedFirstIsIndistinguishable
		}
		t.Run(label+"/before the console minted one", func(t *testing.T) {
			logged := captureLog(t)
			repo, certs := newLookupStore(t)
			certID := plant(t, repo, plantedRow{caseVariant(caCertCredentialName), models.ConsoleRowType}, certPEM)
			keyID := plant(t, repo, row, keyPEM)
			before := rowCount(t, repo)

			ca, err := NewCertIssuer(repo, certs, "", "", AgentPackage{}).CA(ctx)
			assert.Nil(t, ca)
			assertConflict(t, err, logged.String(), []string{certID, keyID}, certPEM, keyPEM)
			assert.Equal(t, before, rowCount(t, repo), "no CA may be minted next to planted rows")
		})
	}
}

// TestDataKeyFailsClosedOnLookalikeRows — a data-key row the console did not
// write makes KeyFor and EnsureDataKey fail instead of returning it, and
// EnsureDataKey mints nothing next to it.
func TestDataKeyFailsClosedOnLookalikeRows(t *testing.T) {
	const qube = "6b1f0c2e-0000-4000-8000-000000000001"
	name := dataKeyCredentialPrefix + qube
	ctx := context.Background()

	for label, row := range lookalikes(name, "qubes-air-luKs-key-"+qube) {
		t.Run(label+"/after the key exists", func(t *testing.T) {
			logged := captureLog(t)
			repo, _ := newLookupStore(t)
			keys := NewDataKeyManager(repo)
			genuine, err := keys.EnsureDataKey(ctx, qube)
			require.NoError(t, err)
			plantedID := plant(t, repo, row, "attacker-known-key")

			fresh := NewDataKeyManager(repo)
			key, found, err := fresh.KeyFor(ctx, qube)
			assertConflict(t, err, logged.String(), []string{plantedID}, "attacker-known-key", genuine)
			assert.False(t, found)
			assert.Empty(t, key)
			key, err = fresh.EnsureDataKey(ctx, qube)
			assert.ErrorIs(t, err, models.ErrConsoleRowConflict)
			assert.Empty(t, key)
		})
		if label == "exact-name duplicate" {
			continue // alone, it is the console's own row; see TestExactRowPlantedFirstIsIndistinguishable
		}
		t.Run(label+"/before the console minted one", func(t *testing.T) {
			logged := captureLog(t)
			repo, _ := newLookupStore(t)
			plantedID := plant(t, repo, row, "attacker-known-key")
			before := rowCount(t, repo)

			key, err := NewDataKeyManager(repo).EnsureDataKey(ctx, qube)
			assertConflict(t, err, logged.String(), []string{plantedID}, "attacker-known-key")
			assert.Empty(t, key)
			assert.Equal(t, before, rowCount(t, repo), "no key may be minted next to a planted row")
		})
	}
}

// TestLegacyMasterFailsClosedOnLookalikeRows — the same rule for the legacy
// master: a look-alike master must not derive the key a legacy disk is
// migrated from.
func TestLegacyMasterFailsClosedOnLookalikeRows(t *testing.T) {
	ctx := context.Background()
	for label, row := range lookalikes(dataMasterCredentialName, "qubes-air-luKs-master") {
		t.Run(label, func(t *testing.T) {
			logged := captureLog(t)
			repo, _ := newLookupStore(t)
			genuineID := plant(t, repo, plantedRow{dataMasterCredentialName, models.ConsoleRowType}, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			plantedID := plant(t, repo, row, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

			key, err := NewDataKeyManager(repo).LegacyKeyFor(ctx, "q1")
			assertConflict(t, err, logged.String(), []string{genuineID, plantedID}, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
			assert.Empty(t, key)
		})
	}
}

// TestPlantedCAPairIsRefused is the review's reproduction: a CA pair planted
// as {"qubeſ-air-ca-cert", pki} and {"qubes-air-ca-key", pki} is invisible to
// the operator's list and used to be the CA a restarted console loaded. It is
// now refused both next to the genuine CA and on a store that has none yet.
func TestPlantedCAPairIsRefused(t *testing.T) {
	attacker, err := pki.NewCA("attacker", time.Hour)
	require.NoError(t, err)
	certPEM, keyPEM, err := attacker.MarshalCA()
	require.NoError(t, err)
	ctx := context.Background()

	for _, genuineFirst := range []bool{true, false} {
		repo, certs := newLookupStore(t)
		if genuineFirst {
			_, err := NewCertIssuer(repo, certs, "", "", AgentPackage{}).CA(ctx)
			require.NoError(t, err)
		}
		plant(t, repo, plantedRow{"qube\u017f-air-ca-cert", models.ConsoleRowType}, certPEM)
		plant(t, repo, plantedRow{caKeyCredentialName, models.ConsoleRowType}, keyPEM)

		visible, err := NewCredentialService(repo).List(ctx)
		require.NoError(t, err)
		assert.Empty(t, visible, "the planted pair is not an operator row")

		ca, err := NewCertIssuer(repo, certs, "", "", AgentPackage{}).CA(ctx)
		require.ErrorIs(t, err, models.ErrConsoleRowConflict, "genuine CA first: %v", genuineFirst)
		assert.Nil(t, ca)
	}
}

// TestExactRowPlantedFirstIsIndistinguishable pins the limit of the lookup:
// one row with exactly the console's name and type, stored before the console
// wrote its own, is all the console ever sees, so it is used. The store cannot
// tell it apart; only the audit trail (a 201 on POST /api/v1/credentials) or
// the missing "minted a per-qube data key" log line can. The credentials API
// no longer accepts such a row, and docs/security-controls.md describes how to
// look for one planted earlier.
func TestExactRowPlantedFirstIsIndistinguishable(t *testing.T) {
	ctx := context.Background()
	repo, _ := newLookupStore(t)
	plant(t, repo, plantedRow{dataKeyCredentialPrefix + "q1", models.ConsoleRowType}, "planted-before-mint")

	key, err := NewDataKeyManager(repo).EnsureDataKey(ctx, "q1")
	require.NoError(t, err)
	assert.Equal(t, "planted-before-mint", key)
}

// TestConsoleLookupsStillServeTheirOwnRows — with exactly the rows the console
// writes, next to unrelated operator rows, every lookup works.
func TestConsoleLookupsStillServeTheirOwnRows(t *testing.T) {
	ctx := context.Background()
	repo, certs := newLookupStore(t)
	plant(t, repo, plantedRow{"pve-prod", "proxmox"}, "root@pam!ci=abc")
	plant(t, repo, plantedRow{"my-qubes-air-ca-key", "other"}, "unrelated")

	ca, err := NewCertIssuer(repo, certs, "", "", AgentPackage{}).CA(ctx)
	require.NoError(t, err)
	again, err := NewCertIssuer(repo, certs, "", "", AgentPackage{}).CA(ctx)
	require.NoError(t, err)
	assert.Equal(t, ca.Cert.Raw, again.Cert.Raw)

	keys := NewDataKeyManager(repo)
	key, err := keys.EnsureDataKey(ctx, "q1")
	require.NoError(t, err)
	got, found, err := NewDataKeyManager(repo).KeyFor(ctx, "q1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, key, got)
}

// TestPurgeDeletesWhatTheLookupWouldAnswerTo — purge's crypto-shred removes
// every row answering to the qube's key or marker name under the lookup's own
// comparison, including look-alikes, and leaves alone rows that do not: an
// operator row that only lower-cases to the key name (U+0130 lower-cases to
// "i" but does not fold to it) and another qube's key.
func TestPurgeDeletesWhatTheLookupWouldAnswerTo(t *testing.T) {
	ctx := context.Background()
	repo, _ := newLookupStore(t)
	keys := NewDataKeyManager(repo)
	_, err := keys.EnsureDataKey(ctx, "q1")
	require.NoError(t, err)
	require.NoError(t, keys.MarkMigrationPending(ctx, "q1"))
	_, err = keys.EnsureDataKey(ctx, "q2")
	require.NoError(t, err)

	name := dataKeyCredentialPrefix + "q1"
	for _, row := range []plantedRow{
		{caseVariant(name), models.ConsoleRowType},
		{longSVariant(name), "other"},
		{"qubes-air-luKs-key-q1", models.ConsoleRowType},
		{" " + name, models.ConsoleRowType},
		{caseVariant(migrationMarkerPrefix + "q1"), models.ConsoleRowType},
	} {
		plant(t, repo, row, "look-alike")
	}
	dottedI := "qubes-aİr-luks-key-q1"
	require.Equal(t, name, strings.ToLower(dottedI), "the case the old ToLower comparison got wrong")
	require.False(t, models.IsConsoleCredential(dottedI, "other"), "it is an operator row")
	operatorID := plant(t, repo, plantedRow{dottedI, "other"}, "operator-secret")

	require.NoError(t, keys.DeleteDataKey(ctx, "q1"))

	left, err := repo.List(ctx)
	require.NoError(t, err)
	names := make([]string, 0, len(left))
	for _, c := range left {
		names = append(names, c.Name)
		assert.False(t, models.MatchesConsoleName(c.Name, name), "%q answers to the shredded key and must be gone", c.Name)
		assert.False(t, models.MatchesConsoleName(c.Name, migrationMarkerPrefix+"q1"), "%q answers to the marker and must be gone", c.Name)
	}
	assert.Contains(t, names, dottedI, "an operator row that only lower-cases to the key name must survive purge")
	assert.Contains(t, names, dataKeyCredentialPrefix+"q2", "another qube's key must survive")
	secret, err := repo.GetSecret(ctx, operatorID)
	require.NoError(t, err)
	assert.Equal(t, "operator-secret", secret)
}

// TestClearMigrationPendingRemovesEveryMarker — a duplicate marker must not
// keep a qube marked after the legacy slot is verified gone.
func TestClearMigrationPendingRemovesEveryMarker(t *testing.T) {
	ctx := context.Background()
	repo, _ := newLookupStore(t)
	keys := NewDataKeyManager(repo)
	require.NoError(t, keys.MarkMigrationPending(ctx, "q1"))
	require.NoError(t, keys.MarkMigrationPending(ctx, "q1"), "marking twice is idempotent")
	assert.Equal(t, 1, rowCount(t, repo))
	plant(t, repo, plantedRow{caseVariant(migrationMarkerPrefix + "q1"), models.ConsoleRowType}, "pending")

	require.NoError(t, keys.ClearMigrationPending(ctx, "q1"))
	pending, err := keys.MigrationPending(ctx, "q1")
	require.NoError(t, err)
	assert.False(t, pending)
	assert.Zero(t, rowCount(t, repo))
}

// TestRevocationDocumentRefusesPlantedCARows — the public revocation document
// is signed with the CA read through the shared fail-closed loader: a planted
// long-s certificate or a duplicate key next to the genuine CA yields no
// document at all rather than one signed with the look-alike.
func TestRevocationDocumentRefusesPlantedCARows(t *testing.T) {
	attacker, err := pki.NewCA("attacker", time.Hour)
	require.NoError(t, err)
	certPEM, keyPEM, err := attacker.MarshalCA()
	require.NoError(t, err)
	ctx := context.Background()

	for label, row := range map[string]struct {
		planted plantedRow
		secret  string
	}{
		"long-s certificate": {plantedRow{longSVariant(caCertCredentialName), models.ConsoleRowType}, certPEM},
		"duplicate key":      {plantedRow{caKeyCredentialName, models.ConsoleRowType}, keyPEM},
	} {
		t.Run(label, func(t *testing.T) {
			logged := captureLog(t)
			repo, certs := newLookupStore(t)
			issuer := NewCertIssuer(repo, certs, "", "", AgentPackage{})
			_, err := issuer.CA(ctx)
			require.NoError(t, err)
			doc, err := issuer.RevocationDocument(ctx)
			require.NoError(t, err, "the genuine CA alone signs a document")
			require.NotEmpty(t, doc)

			plant(t, repo, row.planted, row.secret)
			// The document is public (GET /pki/revocations, 5 rps per
			// client): every read refuses, but the standing conflict is
			// logged once.
			for range 5 {
				doc, err = NewCertIssuer(repo, certs, "", "", AgentPackage{}).RevocationDocument(ctx)
				assert.ErrorIs(t, err, models.ErrConsoleRowConflict)
				assert.Nil(t, doc)
			}
			assert.Equal(t, 1, strings.Count(logged.String(), "SECURITY: pki:"))
		})
	}
}

// TestZoneReferencingAConsoleRowSendsNothing pins the reasoning behind the
// zone check in the docs: a zone saved before credential_id was validated may
// reference one of the console's own rows, and the resolver must never turn
// such a value into a Proxmox credential. Each kind of row the console writes,
// in several fresh instances, parses to nothing, so the resolver refuses and
// the adapter is never built with it.
func TestZoneReferencingAConsoleRowSendsNothing(t *testing.T) {
	ctx := context.Background()
	repo, certs := newLookupStore(t)
	db := certTestDB(t)
	zones := repository.NewZoneRepository(db)
	resolve := NewZoneCredentialResolver(zones, repo)
	keys := NewDataKeyManager(repo)

	for i := range 5 {
		qube := fmt.Sprintf("q%d", i)
		_, err := keys.EnsureDataKey(ctx, qube)
		require.NoError(t, err)
		require.NoError(t, keys.MarkMigrationPending(ctx, qube))
	}
	master := make([]byte, 32)
	_, err := rand.Read(master)
	require.NoError(t, err)
	plant(t, repo, plantedRow{dataMasterCredentialName, models.ConsoleRowType}, base64.RawURLEncoding.EncodeToString(master))
	_, err = NewCertIssuer(repo, certs, "", "", AgentPackage{}).CA(ctx)
	require.NoError(t, err)

	rows, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 13, "CA pair, master, and a key and a marker for five qubes")
	for _, row := range rows {
		require.True(t, models.IsConsoleCredential(row.Name, row.Type))
		secret, err := repo.GetSecret(ctx, row.ID)
		require.NoError(t, err)
		assert.Equal(t, scheduler.Credentials{}, parseProxmoxSecret(secret), "%s must not parse as a Proxmox credential", row.Name)

		zoneID := "zone-" + row.ID
		now := time.Now()
		require.NoError(t, zones.Create(ctx, &models.Zone{
			ID: zoneID, Name: zoneID, Type: models.ZoneTypeProxmox,
			Config: models.ZoneConfig{
				Endpoint: "https://attacker.example:8006",
				Proxmox:  &models.ProxmoxZoneConfig{CredentialID: row.ID},
			},
			CreatedAt: now, UpdatedAt: now,
		}))
		creds, err := resolve(ctx, zoneID)
		require.Error(t, err, "%s: the resolver must refuse", row.Name)
		assert.Equal(t, scheduler.Credentials{}, creds)
		assert.NotContains(t, err.Error(), secret, "the refusal must not carry the value")
	}
}
