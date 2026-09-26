package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// credStoreRig is one real encrypted credential store holding every kind of
// console row, written by the production writers, next to operator rows.
type credStoreRig struct {
	repo   *repository.CredentialRepository
	svc    *CredentialService
	keys   *DataKeyManager
	issuer *CertIssuer
	// consoleIDs are the IDs of rows the operator view must not address.
	consoleIDs []string
	// operatorNames are the rows it must list.
	operatorNames []string
}

func newCredStoreRig(t *testing.T) credStoreRig {
	t.Helper()
	ctx := context.Background()
	db := certTestDB(t)
	kr, err := keyring.NewSingle([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	repo := repository.NewCredentialRepository(db, kr)
	r := credStoreRig{
		repo:   repo,
		svc:    NewCredentialService(repo),
		keys:   NewDataKeyManager(repo),
		issuer: NewCertIssuer(repo, repository.NewAgentCertRepository(db), "", "", AgentPackage{}),
	}

	// The console's own rows, through the code that writes them in production.
	_, err = r.issuer.CA(ctx)
	require.NoError(t, err)
	_, err = r.keys.EnsureDataKey(ctx, "q1")
	require.NoError(t, err)
	require.NoError(t, r.keys.MarkMigrationPending(ctx, "q1"))
	// The console no longer mints a legacy master; a deployment that predates
	// per-qube keys still has one.
	r.create(t, dataMasterCredentialName, dataMasterCredentialType, base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	// Rows stored before the namespace was enforced: they are hidden too, the
	// first because its type is the console's, the second because its name is
	// in the console's namespace.
	r.create(t, "legacy-pki-row", "pki", "x")
	r.create(t, "QUBES-AIR-old-token", "proxmox", "x")

	list, err := repo.List(ctx)
	require.NoError(t, err)
	for _, c := range list {
		r.consoleIDs = append(r.consoleIDs, c.ID)
	}
	require.Len(t, r.consoleIDs, 7, "CA cert+key, DEK, marker, master and two legacy rows")

	for _, op := range []struct{ name, typ string }{
		{"pve-prod", "proxmox"},
		{"my-qubes-air-token", "api_key"},
		{"qubes-airport", "other"},
	} {
		r.create(t, op.name, op.typ, "secret-"+op.name)
		r.operatorNames = append(r.operatorNames, op.name)
	}
	return r
}

func (r credStoreRig) create(t *testing.T, name, typ, secret string) *models.Credential {
	t.Helper()
	c, err := r.repo.Create(context.Background(), models.CredentialCreateRequest{Name: name, Type: typ, SecretValue: secret})
	require.NoError(t, err)
	return c
}

// rowState is everything about a stored row an update or delete could change.
type rowState struct {
	row    models.Credential
	secret string
}

func (r credStoreRig) state(t *testing.T, id string) rowState {
	t.Helper()
	ctx := context.Background()
	row, err := r.repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, row, "row %s must still exist", id)
	secret, err := r.repo.GetSecret(ctx, id)
	require.NoError(t, err)
	row.LastUsed = nil // GetSecret itself stamps last_used
	return rowState{row: *row, secret: secret}
}

func strPtr(s string) *string { return &s }

// TestConsoleWritersStayInTheNamespace — every row the console writes for
// itself is classified as a console row. A new console secret written under a
// name and type outside the namespace would fail here instead of appearing in
// the operator's API.
func TestConsoleWritersStayInTheNamespace(t *testing.T) {
	r := newCredStoreRig(t)
	list, err := r.repo.List(context.Background())
	require.NoError(t, err)
	for _, c := range list {
		if c.Name == "legacy-pki-row" || c.Name == "QUBES-AIR-old-token" || !models.IsConsoleCredential(c.Name, c.Type) {
			continue
		}
		assert.True(t, strings.HasPrefix(c.Name, models.ConsoleRowNamePrefix), "console row %q must be named in the namespace", c.Name)
		assert.Equal(t, models.ConsoleRowType, c.Type, "console row %q must carry the console type", c.Name)
	}
	for _, name := range []string{
		caCertCredentialName, caKeyCredentialName, dataMasterCredentialName,
		dataKeyCredentialPrefix + "q1", migrationMarkerPrefix + "q1",
	} {
		assert.True(t, models.IsConsoleCredential(name, ""), "lookup name %q must be reserved by name alone", name)
	}
	assert.Equal(t, models.ConsoleRowType, caCredentialType)
	assert.Equal(t, models.ConsoleRowType, dataMasterCredentialType)
}

func TestCredentialServiceListShowsOnlyOperatorRows(t *testing.T) {
	r := newCredStoreRig(t)
	list, err := r.svc.List(context.Background())
	require.NoError(t, err)
	names := make([]string, 0, len(list))
	for _, c := range list {
		names = append(names, c.Name)
	}
	assert.ElementsMatch(t, r.operatorNames, names)
}

// TestCredentialServiceRefusesConsoleRowIDs — a console row's ID cannot be
// read, updated or deleted through the operator view, the refusal matches
// "not found", and the row is left exactly as it was.
func TestCredentialServiceRefusesConsoleRowIDs(t *testing.T) {
	r := newCredStoreRig(t)
	ctx := context.Background()
	for _, id := range r.consoleIDs {
		before := r.state(t, id)

		_, err := r.svc.GetByID(ctx, id)
		assert.ErrorIs(t, err, ErrConsoleCredential)
		assert.ErrorIs(t, err, ErrCredentialNotFound, "a console row must read as not found")

		_, err = r.svc.Update(ctx, id, models.CredentialUpdateRequest{
			Name: strPtr("renamed"), Description: strPtr("changed"), SecretValue: strPtr("overwritten"),
		})
		assert.ErrorIs(t, err, ErrConsoleCredential)

		assert.ErrorIs(t, r.svc.Delete(ctx, id), ErrConsoleCredential)

		assert.Equal(t, before, r.state(t, id), "a refused request must not change the row")
	}
}

func TestCredentialServiceMissingIDIsNotAConsoleRow(t *testing.T) {
	r := newCredStoreRig(t)
	ctx := context.Background()

	_, err := r.svc.GetByID(ctx, "no-such-id")
	assert.ErrorIs(t, err, ErrCredentialNotFound)
	assert.NotErrorIs(t, err, ErrConsoleCredential)

	_, err = r.svc.Update(ctx, "no-such-id", models.CredentialUpdateRequest{Description: strPtr("x")})
	assert.ErrorIs(t, err, ErrCredentialNotFound)
	assert.NotErrorIs(t, err, ErrConsoleCredential)

	err = r.svc.Delete(ctx, "no-such-id")
	assert.ErrorIs(t, err, ErrCredentialNotFound)
	assert.NotErrorIs(t, err, ErrConsoleCredential)
}

// TestCredentialServiceRefusesReservedNames — an operator row can neither be
// created nor renamed into the console's namespace, where the console's name
// lookups would find it in place of its own secret.
func TestCredentialServiceRefusesReservedNames(t *testing.T) {
	r := newCredStoreRig(t)
	ctx := context.Background()
	count := func() int {
		all, err := r.repo.List(ctx)
		require.NoError(t, err)
		return len(all)
	}
	stored := count()

	for _, req := range []models.CredentialCreateRequest{
		{Name: caKeyCredentialName, Type: "other"},
		{Name: "QUBES-AIR-CA-CERT", Type: "other"},
		{Name: dataKeyCredentialPrefix + "q2", Type: "other"},
		{Name: migrationMarkerPrefix + "q1", Type: "other"},
		{Name: "qubeſ-air-luks-master", Type: "other"},
		{Name: " qubes-air-ca-key", Type: "other"},
		{Name: "ordinary", Type: "pki"},
		{Name: "ordinary", Type: "PKI"},
	} {
		req.SecretValue = "attacker-chosen"
		_, err := r.svc.Create(ctx, req)
		assert.ErrorIs(t, err, ErrReservedCredential, "create %q/%q", req.Name, req.Type)
	}
	assert.Equal(t, stored, count(), "a refused create must store nothing")

	op, err := r.svc.Create(ctx, models.CredentialCreateRequest{Name: "zone-b", Type: "proxmox", SecretValue: "v"})
	require.NoError(t, err)
	before := r.state(t, op.ID)
	_, err = r.svc.Update(ctx, op.ID, models.CredentialUpdateRequest{Name: strPtr(caCertCredentialName)})
	assert.ErrorIs(t, err, ErrReservedCredential)
	assert.Equal(t, before, r.state(t, op.ID), "a refused rename must not change the row")

	// The answer to a reserved rename does not depend on what the ID names.
	for _, id := range []string{"no-such-id", r.consoleIDs[0]} {
		_, err = r.svc.Update(ctx, id, models.CredentialUpdateRequest{Name: strPtr("qubes-air-x")})
		assert.ErrorIs(t, err, ErrReservedCredential)
	}
}

// TestCredentialServiceCannotShadowConsoleSecrets is the attack the reserved
// namespace closes. The console finds its secrets by case-insensitive name and
// takes the newest match, so before this view refused such names, a control
// token could store its own CA under "Qubes-Air-CA-Cert"/"-Key" and a restarted
// console would load it, or store a data key for a qube before the console
// minted one and have the disk formatted with a key the caller knows.
func TestCredentialServiceCannotShadowConsoleSecrets(t *testing.T) {
	r := newCredStoreRig(t)
	ctx := context.Background()
	realCA, err := r.issuer.CA(ctx)
	require.NoError(t, err)

	attackerCA, err := pki.NewCA("attacker", time.Hour)
	require.NoError(t, err)
	certPEM, keyPEM, err := attackerCA.MarshalCA()
	require.NoError(t, err)
	for _, req := range []models.CredentialCreateRequest{
		{Name: "Qubes-Air-CA-Cert", Type: "other", SecretValue: certPEM},
		{Name: "Qubes-Air-CA-Key", Type: "other", SecretValue: keyPEM},
		{Name: dataKeyCredentialPrefix + "q2", Type: "other", SecretValue: "attacker-known-key"},
	} {
		_, err := r.svc.Create(ctx, req)
		assert.ErrorIs(t, err, ErrReservedCredential)
	}

	loaded, err := NewCertIssuer(r.repo, r.issuer.certs, "", "", AgentPackage{}).CA(ctx)
	require.NoError(t, err)
	assert.Equal(t, realCA.Cert.Raw, loaded.Cert.Raw, "a restarted console must load its own CA")
	key, err := r.keys.EnsureDataKey(ctx, "q2")
	require.NoError(t, err)
	assert.NotEqual(t, "attacker-known-key", key, "a new qube's data key must be minted by the console")
}

func TestCredentialServiceManagesOperatorRows(t *testing.T) {
	r := newCredStoreRig(t)
	ctx := context.Background()

	created, err := r.svc.Create(ctx, models.CredentialCreateRequest{
		Name: "pve-lab", Type: "proxmox", Description: "lab", SecretValue: "root@pam!ci=one",
	})
	require.NoError(t, err)

	got, err := r.svc.GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "pve-lab", got.Name)

	updated, err := r.svc.Update(ctx, created.ID, models.CredentialUpdateRequest{
		Name: strPtr("pve-lab-2"), SecretValue: strPtr("root@pam!ci=two"),
	})
	require.NoError(t, err)
	assert.Equal(t, "pve-lab-2", updated.Name)
	secret, err := r.repo.GetSecret(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "root@pam!ci=two", secret)

	require.NoError(t, r.svc.Delete(ctx, created.ID))
	_, err = r.svc.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, ErrCredentialNotFound)
	assert.ErrorIs(t, r.svc.Delete(ctx, created.ID), ErrCredentialNotFound, "a repeated delete is not found")
}

// TestConsoleFlowsSurviveRefusedAPIRequests — after every console row has been
// attacked through the operator view, the console's own paths still find
// them: the CA a restarted console loads is the same one, the data key and
// its migration marker are intact, and purge still crypto-shreds through the
// repository.
func TestConsoleFlowsSurviveRefusedAPIRequests(t *testing.T) {
	r := newCredStoreRig(t)
	ctx := context.Background()
	ca, err := r.issuer.CA(ctx)
	require.NoError(t, err)
	key, found, err := r.keys.KeyFor(ctx, "q1")
	require.NoError(t, err)
	require.True(t, found)

	for _, id := range r.consoleIDs {
		assert.Error(t, r.svc.Delete(ctx, id))
	}

	restarted := NewCertIssuer(r.repo, r.issuer.certs, "", "", AgentPackage{})
	reloaded, err := restarted.CA(ctx)
	require.NoError(t, err, "the CA must load, not read as half-present")
	assert.Equal(t, ca.Cert.Raw, reloaded.Cert.Raw, "a restarted console must load the same CA")

	fresh := NewDataKeyManager(r.repo)
	got, found, err := fresh.KeyFor(ctx, "q1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, key, got)
	pending, err := fresh.MigrationPending(ctx, "q1")
	require.NoError(t, err)
	assert.True(t, pending)
	_, err = fresh.LegacyKeyFor(ctx, "q1")
	require.NoError(t, err, "the legacy master must still be readable for migration")

	require.NoError(t, fresh.DeleteDataKey(ctx, "q1"), "purge's crypto-shred goes through the repository")
	_, found, err = fresh.KeyFor(ctx, "q1")
	require.NoError(t, err)
	assert.False(t, found)
	pending, err = fresh.MigrationPending(ctx, "q1")
	require.NoError(t, err)
	assert.False(t, pending)
}

func TestCredentialServicePassesStorageErrorsThrough(t *testing.T) {
	kr, err := keyring.NewSingle([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	svc := NewCredentialService(repository.NewCredentialRepository(closedDB(t), kr))
	ctx := context.Background()

	_, err = svc.List(ctx)
	assert.Error(t, err)
	_, err = svc.GetByID(ctx, "x")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrCredentialNotFound, "a storage failure is not a missing row")
	_, err = svc.Update(ctx, "x", models.CredentialUpdateRequest{})
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrCredentialNotFound)
	err = svc.Delete(ctx, "x")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrCredentialNotFound)
}
