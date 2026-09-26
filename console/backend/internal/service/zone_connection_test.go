package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCredentialRefs answers like the operator's view of the store: known IDs
// resolve, "console-row" is a console row, anything else is missing, and
// "broken" is a storage failure.
type fakeCredentialRefs map[string]bool

func (f fakeCredentialRefs) GetByID(_ context.Context, id string) (*models.Credential, error) {
	switch {
	case id == "console-row":
		return nil, ErrConsoleCredential
	case id == "broken":
		return nil, errors.New("database is locked")
	case f[id]:
		return &models.Credential{ID: id}, nil
	}
	return nil, ErrCredentialNotFound
}

func newRefZoneService(t *testing.T, refs ZoneCredentialRefs) (ZoneService, repository.ZoneRepository) {
	t.Helper()
	f, err := os.CreateTemp("", "zone-conn-*.db")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	cfg := database.DefaultConfig()
	cfg.DSN = f.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
		_ = os.Remove(f.Name())
	})
	zoneRepo := repository.NewZoneRepository(db)
	var opts []ZoneServiceOption
	if refs != nil {
		opts = append(opts, WithCredentialRefs(refs))
	}
	return NewZoneService(zoneRepo, repository.NewQubeRepository(db), proxmoxOnlyAdapters(t), opts...), zoneRepo
}

func proxmoxConfig(endpoint, credentialID string) models.ZoneConfig {
	return models.ZoneConfig{
		Endpoint: endpoint,
		Proxmox:  &models.ProxmoxZoneConfig{CredentialID: credentialID, CAPEM: "ca-a", Node: "pve1"},
	}
}

func TestZoneCredentialReferenceIsChecked(t *testing.T) {
	ctx := context.Background()
	svc, _ := newRefZoneService(t, fakeCredentialRefs{"cred-a": true, "cred-gcp": true})

	create := func(cfg models.ZoneConfig) error {
		_, err := svc.Create(ctx, &models.ZoneCreateRequest{Name: "z", Type: models.ZoneTypeProxmox, Config: cfg})
		return err
	}
	require.NoError(t, create(proxmoxConfig("https://pve.example:8006", "cred-a")))
	require.NoError(t, create(proxmoxConfig("https://pve.example:8006", "")), "a zone without a credential is allowed")

	err := create(proxmoxConfig("https://pve.example:8006", "no-such-credential"))
	assert.ErrorIs(t, err, ErrZoneCredentialNotFound)
	assert.NotErrorIs(t, err, ErrConsoleCredential)

	err = create(proxmoxConfig("https://pve.example:8006", "console-row"))
	assert.ErrorIs(t, err, ErrZoneCredentialNotFound, "a console row reads as not found")
	assert.ErrorIs(t, err, ErrConsoleCredential, "but stays distinguishable for the audit trail")

	err = create(models.ZoneConfig{GCP: &models.GCPZoneConfig{CredentialID: "no-such-credential"}})
	assert.ErrorIs(t, err, ErrZoneCredentialNotFound, "the GCP reference is checked too")
	require.NoError(t, create(models.ZoneConfig{GCP: &models.GCPZoneConfig{CredentialID: "cred-gcp"}}))

	err = create(proxmoxConfig("https://pve.example:8006", "broken"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrZoneCredentialNotFound, "a storage failure is not a missing credential")

	zone, err := svc.Create(ctx, &models.ZoneCreateRequest{Name: "u", Type: models.ZoneTypeProxmox, Config: proxmoxConfig("https://pve.example:8006", "cred-a")})
	require.NoError(t, err)
	_, err = svc.Update(ctx, zone.ID, &models.ZoneUpdateRequest{Config: ptrConfig(proxmoxConfig("https://pve.example:8006", "console-row"))})
	assert.ErrorIs(t, err, ErrZoneCredentialNotFound, "update is checked like create")
	stored, err := svc.GetByID(ctx, zone.ID)
	require.NoError(t, err)
	assert.Equal(t, "cred-a", stored.Config.Proxmox.CredentialID, "a refused update changes nothing")
}

// TestZoneCredentialReferenceFailsClosedWithoutAChecker — a service built
// without WithCredentialRefs cannot tell a credential from a console row, so
// it refuses every config that names one.
func TestZoneCredentialReferenceFailsClosedWithoutAChecker(t *testing.T) {
	svc, _ := newRefZoneService(t, nil)
	_, err := svc.Create(context.Background(), &models.ZoneCreateRequest{
		Name: "z", Type: models.ZoneTypeProxmox, Config: proxmoxConfig("https://pve.example:8006", "cred-a"),
	})
	assert.ErrorIs(t, err, ErrZoneCredentialNotFound)
}

func ptrConfig(cfg models.ZoneConfig) *models.ZoneConfig { return &cfg }

// TestZoneScopedUpdateKeepsTheConnection — a zone-scoped caller can rename its
// zone and change placement, but every field that decides where the zone's
// credential goes is refused, and the refusal comes before the credential
// check so it cannot be used to probe other credential IDs.
func TestZoneScopedUpdateKeepsTheConnection(t *testing.T) {
	ctx := context.Background()
	svc, _ := newRefZoneService(t, fakeCredentialRefs{"cred-a": true, "cred-b": true})
	base := proxmoxConfig("https://pve.example:8006", "cred-a")
	base.GCP = &models.GCPZoneConfig{CredentialID: "cred-a", IdentityBucket: "bucket-a", ServiceAccountEmail: "sa@a"}
	zone, err := svc.Create(ctx, &models.ZoneCreateRequest{Name: "zone-a", Type: models.ZoneTypeProxmox, Config: base})
	require.NoError(t, err)

	changed := func(mutate func(*models.ZoneConfig)) *models.ZoneConfig {
		cfg := base
		px, gcp := *base.Proxmox, *base.GCP
		cfg.Proxmox, cfg.GCP = &px, &gcp
		mutate(&cfg)
		return &cfg
	}
	for label, cfg := range map[string]*models.ZoneConfig{
		"endpoint":              changed(func(c *models.ZoneConfig) { c.Endpoint = "https://attacker.example" }),
		"proxmox credential":    changed(func(c *models.ZoneConfig) { c.Proxmox.CredentialID = "cred-b" }),
		"unknown credential":    changed(func(c *models.ZoneConfig) { c.Proxmox.CredentialID = "no-such-credential" }),
		"proxmox CA":            changed(func(c *models.ZoneConfig) { c.Proxmox.CAPEM = "attacker-ca" }),
		"proxmox block dropped": changed(func(c *models.ZoneConfig) { c.Proxmox = nil }),
		"gcp credential":        changed(func(c *models.ZoneConfig) { c.GCP.CredentialID = "cred-b" }),
		"gcp identity bucket":   changed(func(c *models.ZoneConfig) { c.GCP.IdentityBucket = "attacker-bucket" }),
		"gcp service account":   changed(func(c *models.ZoneConfig) { c.GCP.ServiceAccountEmail = "sa@attacker" }),
	} {
		_, err := svc.UpdateKeepingConnection(ctx, zone.ID, &models.ZoneUpdateRequest{Config: cfg})
		assert.ErrorIs(t, err, ErrZoneConnectionLocked, label)
		stored, err := svc.GetByID(ctx, zone.ID)
		require.NoError(t, err)
		assert.Equal(t, base, stored.Config, "%s: a refused update changes nothing", label)
	}

	placement := changed(func(c *models.ZoneConfig) { c.Proxmox.Node = "pve2"; c.Proxmox.DatastoreID = "ceph" })
	renamed := "zone-a-renamed"
	updated, err := svc.UpdateKeepingConnection(ctx, zone.ID, &models.ZoneUpdateRequest{Name: &renamed, Config: placement})
	require.NoError(t, err)
	assert.Equal(t, "pve2", updated.Config.Proxmox.Node)
	assert.Equal(t, renamed, updated.Name)
	_, err = svc.UpdateKeepingConnection(ctx, zone.ID, &models.ZoneUpdateRequest{Name: &renamed})
	require.NoError(t, err, "a rename alone does not touch the connection")

	moved := changed(func(c *models.ZoneConfig) {
		c.Endpoint = "https://pve2.example:8006"
		c.Proxmox.CredentialID = "cred-b"
	})
	updated, err = svc.Update(ctx, zone.ID, &models.ZoneUpdateRequest{Config: moved})
	require.NoError(t, err, "a fleet-wide caller may repoint the zone")
	assert.Equal(t, "cred-b", updated.Config.Proxmox.CredentialID)
}
