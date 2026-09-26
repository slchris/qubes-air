package repository

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// caRows stores a CA pair under the given names and type.
func caRows(t *testing.T, repo *CredentialRepository, ca *pki.CA, certName, keyName, typ string) (certID, keyID string) {
	t.Helper()
	ctx := context.Background()
	certPEM, keyPEM, err := ca.MarshalCA()
	require.NoError(t, err)
	if certName != "" {
		c, err := repo.Create(ctx, models.CredentialCreateRequest{Name: certName, Type: typ, SecretValue: certPEM})
		require.NoError(t, err)
		certID = c.ID
	}
	if keyName != "" {
		k, err := repo.Create(ctx, models.CredentialCreateRequest{Name: keyName, Type: typ, SecretValue: keyPEM})
		require.NoError(t, err)
		keyID = k.ID
	}
	return certID, keyID
}

// TestLoadConsoleCAFailsClosed — the loader the CLI tools and the revocation
// document share returns the genuine CA only when the store holds exactly the
// rows the console writes. A planted look-alike, next to the genuine pair or
// on a store with none, is a conflict naming the rows, and no key material
// comes back.
func TestLoadConsoleCAFailsClosed(t *testing.T) {
	genuine, err := pki.NewCA("genuine", time.Hour)
	require.NoError(t, err)
	attacker, err := pki.NewCA("attacker", time.Hour)
	require.NoError(t, err)
	_, attackerKey, err := attacker.MarshalCA()
	require.NoError(t, err)
	ctx := context.Background()
	newRepo := func(t *testing.T) *CredentialRepository {
		db, cleanup := setupTestDB(t)
		t.Cleanup(cleanup)
		kr, err := keyring.NewSingle([]byte(oldKey))
		require.NoError(t, err)
		return NewCredentialRepository(db, kr)
	}

	t.Run("genuine pair", func(t *testing.T) {
		repo := newRepo(t)
		caRows(t, repo, genuine, models.ConsoleCACertName, models.ConsoleCAKeyName, models.ConsoleRowType)
		ca, err := LoadConsoleCA(ctx, repo)
		require.NoError(t, err)
		assert.Equal(t, genuine.Cert.Raw, ca.Cert.Raw)
	})
	t.Run("absent", func(t *testing.T) {
		ca, err := LoadConsoleCA(ctx, newRepo(t))
		assert.ErrorIs(t, err, models.ErrConsoleRowNotFound)
		assert.Nil(t, ca)
	})

	for label, plant := range map[string]struct{ cert, key, typ string }{
		"duplicate key":      {key: models.ConsoleCAKeyName, typ: models.ConsoleRowType},
		"long-s certificate": {cert: "qubeſ-air-ca-cert", typ: models.ConsoleRowType},
		"Kelvin-sign key":    {key: "qubes-air-ca-Key", typ: models.ConsoleRowType},
		"case-variant pair":  {cert: "QUBES-AIR-CA-CERT", key: "QUBES-AIR-CA-KEY", typ: models.ConsoleRowType},
		"wrong-type key":     {key: models.ConsoleCAKeyName, typ: "other"},
	} {
		for _, withGenuine := range []bool{true, false} {
			name := label + "/next to the genuine pair"
			if !withGenuine {
				name = label + "/on a store without one"
			}
			t.Run(name, func(t *testing.T) {
				repo := newRepo(t)
				if withGenuine {
					caRows(t, repo, genuine, models.ConsoleCACertName, models.ConsoleCAKeyName, models.ConsoleRowType)
				}
				certID, keyID := caRows(t, repo, attacker, plant.cert, plant.key, plant.typ)

				ca, err := LoadConsoleCA(ctx, repo)
				assert.Nil(t, ca)
				if !withGenuine && plant.cert == "" {
					// Only the key was planted: the certificate half is absent.
					assert.ErrorIs(t, err, models.ErrConsoleRowNotFound)
					return
				}
				require.ErrorIs(t, err, models.ErrConsoleRowConflict)
				// The certificate half is read first, so a planted certificate
				// is the one the refusal names.
				planted := certID
				if plant.cert == "" {
					planted = keyID
				}
				assert.Contains(t, err.Error(), planted, "the refusal names the planted row")
				assert.NotContains(t, err.Error(), attackerKey)
			})
		}
	}
}

// TestConsoleSecretSeparatesStorageFailures — a store that cannot be read is
// neither "absent" nor "ambiguous".
func TestConsoleSecretSeparatesStorageFailures(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	kr, err := keyring.NewSingle([]byte(oldKey))
	require.NoError(t, err)
	repo := NewCredentialRepository(db, kr)
	require.NoError(t, db.Close())

	_, err = ConsoleSecret(context.Background(), repo, models.ConsoleCAKeyName)
	require.Error(t, err)
	assert.NotErrorIs(t, err, models.ErrConsoleRowNotFound)
	assert.NotErrorIs(t, err, models.ErrConsoleRowConflict)
}

// TestConsoleSecretLogsAStandingConflictOnce — the lookup runs on every public
// revocation read, so a standing conflict is logged once, not once per call.
// A changed conflict (another row planted) is logged again, and a store with
// a megabyte-long look-alike still logs a bounded line.
func TestConsoleSecretLogsAStandingConflictOnce(t *testing.T) {
	var logged bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&logged)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(flags)
	})

	db, cleanup := setupTestDB(t)
	defer cleanup()
	kr, err := keyring.NewSingle([]byte(oldKey))
	require.NoError(t, err)
	repo := NewCredentialRepository(db, kr)
	ctx := context.Background()
	// A name no other test uses: the once-per-name memory is process-wide.
	name := "qubes-air-luks-key-" + t.Name()
	create := func(n string) {
		_, err := repo.Create(ctx, models.CredentialCreateRequest{Name: n, Type: models.ConsoleRowType, SecretValue: "k"})
		require.NoError(t, err)
	}
	create(name)
	create(strings.Repeat(" ", 1<<20) + name)

	lines := func() int { return strings.Count(logged.String(), "SECURITY: pki:") }
	for range 5 {
		_, err := ConsoleSecret(ctx, repo, name)
		require.ErrorIs(t, err, models.ErrConsoleRowConflict)
	}
	assert.Equal(t, 1, lines(), "a standing conflict is logged once")
	assert.Less(t, logged.Len(), 8<<10, "the logged line is bounded")

	create(strings.ToUpper(name))
	for range 3 {
		_, err := ConsoleSecret(ctx, repo, name)
		require.ErrorIs(t, err, models.ErrConsoleRowConflict)
	}
	assert.Equal(t, 2, lines(), "a changed conflict is logged again, once")
}
