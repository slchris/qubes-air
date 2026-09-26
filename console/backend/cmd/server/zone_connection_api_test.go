package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/handler"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const zoneRoute = "/api/v1/zones/:id"

// zoneFleet is two Proxmox zones, zone-a and zone-b, each referencing its own
// operator credential, behind the production /api/v1 chain. The zone token
// the audited API configures is restricted to zone-a.
type zoneFleet struct {
	zones      repository.ZoneRepository
	credA      string
	credB      string
	consoleRow string
}

func newZoneFleet(t *testing.T, store credentialStore) zoneFleet {
	t.Helper()
	ctx := context.Background()
	create := func(name string) string {
		c, err := store.repo.Create(ctx, models.CredentialCreateRequest{Name: name, Type: "proxmox", SecretValue: "root@pam!" + name + "=s"})
		require.NoError(t, err)
		return c.ID
	}
	f := zoneFleet{zones: store.zones, credA: create("pve-a"), credB: create("pve-b"), consoleRow: store.caKeyID}
	for id, cred := range map[string]string{"zone-a": f.credA, "zone-b": f.credB} {
		now := time.Now()
		require.NoError(t, f.zones.Create(ctx, &models.Zone{
			ID: id, Name: id, Type: models.ZoneTypeProxmox, Status: models.ZoneStatusConnected,
			Config:    zoneConfig("https://"+id+".example:8006", cred),
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	return f
}

func zoneConfig(endpoint, credentialID string) models.ZoneConfig {
	return models.ZoneConfig{
		Endpoint: endpoint,
		Proxmox:  &models.ProxmoxZoneConfig{CredentialID: credentialID, CAPEM: "cluster-ca", Node: "pve1"},
	}
}

func (f zoneFleet) api(t *testing.T, store credentialStore) (*gin.Engine, func() map[string]any) {
	t.Helper()
	registry := provider.NewRegistry()
	require.NoError(t, registry.Register(models.ZoneTypeProxmox, func(context.Context, *models.Zone) (provider.Adapter, error) {
		return nil, errors.New("never built in this test")
	}))
	zoneSvc := service.NewZoneService(f.zones, store.qubes, registry,
		service.WithCredentialRefs(service.NewCredentialService(store.repo)))
	r, buf, _ := auditedAPIWith(t, nil, func(v1 *gin.RouterGroup) {
		handler.NewZoneHandler(zoneSvc).RegisterRoutes(v1)
	})
	return r, func() map[string]any {
		line := onlyAuditLine(t, buf)
		buf.Reset()
		return line
	}
}

func (f zoneFleet) stored(t *testing.T, id string) models.ZoneConfig {
	t.Helper()
	zone, err := f.zones.GetByID(context.Background(), id)
	require.NoError(t, err)
	return zone.Config
}

func updateBody(t *testing.T, name string, cfg models.ZoneConfig) string {
	t.Helper()
	body, err := json.Marshal(models.ZoneUpdateRequest{Name: &name, Config: &cfg})
	require.NoError(t, err)
	return string(body)
}

// TestZoneTokenCannotRepointItsZone is the exfiltration path: a control token
// restricted to zone-a could PUT zone-a with another endpoint (or another
// zone's credential, or its own CA), and the console would send a
// fleet-managed Proxmox credential there. Each such change is refused with
// 403 and audited as denied; renaming and placement changes still work.
func TestZoneTokenCannotRepointItsZone(t *testing.T) {
	store := newCredentialStore(t)
	fleet := newZoneFleet(t, store)
	r, auditLine := fleet.api(t, store)
	zoneToken := bearer(zoneTokenValue)
	before := fleet.stored(t, "zone-a")

	for label, cfg := range map[string]models.ZoneConfig{
		"attacker endpoint":       zoneConfig("https://attacker.example", fleet.credA),
		"another zone credential": zoneConfig("https://zone-a.example:8006", fleet.credB),
		"console row":             zoneConfig("https://zone-a.example:8006", fleet.consoleRow),
		"attacker CA": func() models.ZoneConfig {
			c := zoneConfig("https://zone-a.example:8006", fleet.credA)
			c.Proxmox.CAPEM = "attacker-ca"
			return c
		}(),
	} {
		w := apiRequest(r, http.MethodPut, "/api/v1/zones/zone-a", updateBody(t, "zone-a", cfg), zoneToken)
		assert.Equal(t, http.StatusForbidden, w.Code, label)
		assertAuditFields(t, auditLine(), w, map[string]any{
			"outcome": audit.OutcomeDenied, "subject": "zone-a-control", "zone_scope": "zone-a",
			"route": zoneRoute, "object": "zone-a",
		})
		assert.Equal(t, before, fleet.stored(t, "zone-a"), "%s: the zone must be unchanged", label)
	}

	placement := zoneConfig("https://zone-a.example:8006", fleet.credA)
	placement.Proxmox.Node = "pve2"
	w := apiRequest(r, http.MethodPut, "/api/v1/zones/zone-a", updateBody(t, "zone-a-renamed", placement), zoneToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assertAuditFields(t, auditLine(), w, map[string]any{"outcome": audit.OutcomeSuccess, "subject": "zone-a-control"})
	assert.Equal(t, "pve2", fleet.stored(t, "zone-a").Proxmox.Node)

	w = apiRequest(r, http.MethodPut, "/api/v1/zones/zone-b", updateBody(t, "zone-b", zoneConfig("https://attacker.example", fleet.credB)), zoneToken)
	assert.Equal(t, http.StatusNotFound, w.Code, "another zone stays invisible")
	auditLine()
}

// TestFleetTokenRepointsZonesOnlyToOperatorCredentials — a fleet-wide token
// may move a zone to another endpoint and credential, but not to a credential
// that does not exist or to a console row; both refusals read the same, and
// only the console row is audited as denied.
func TestFleetTokenRepointsZonesOnlyToOperatorCredentials(t *testing.T) {
	store := newCredentialStore(t)
	fleet := newZoneFleet(t, store)
	r, auditLine := fleet.api(t, store)
	admin := bearer(adminTokenValue)
	before := fleet.stored(t, "zone-a")

	missing := apiRequest(r, http.MethodPut, "/api/v1/zones/zone-a",
		updateBody(t, "zone-a", zoneConfig("https://zone-a.example:8006", "no-such-credential")), admin)
	assert.Equal(t, http.StatusUnprocessableEntity, missing.Code)
	assertAuditFields(t, auditLine(), missing, map[string]any{"outcome": audit.OutcomeClientError})

	consoleRow := apiRequest(r, http.MethodPut, "/api/v1/zones/zone-a",
		updateBody(t, "zone-a", zoneConfig("https://zone-a.example:8006", fleet.consoleRow)), admin)
	assert.Equal(t, missing.Code, consoleRow.Code)
	assert.Equal(t, missing.Body.String(), consoleRow.Body.String(), "a console row must read like a missing credential")
	assertAuditFields(t, auditLine(), consoleRow, map[string]any{"outcome": audit.OutcomeDenied})
	assert.Equal(t, before, fleet.stored(t, "zone-a"))

	created := apiRequest(r, http.MethodPost, "/api/v1/zones",
		`{"name":"zone-c","type":"proxmox","config":{"endpoint":"https://c.example","proxmox":{"credential_id":"`+fleet.consoleRow+`"}}}`, admin)
	assert.Equal(t, http.StatusUnprocessableEntity, created.Code, "create is checked too")
	assertAuditFields(t, auditLine(), created, map[string]any{"outcome": audit.OutcomeDenied})

	moved := apiRequest(r, http.MethodPut, "/api/v1/zones/zone-a",
		updateBody(t, "zone-a", zoneConfig("https://pve-new.example:8006", fleet.credB)), admin)
	require.Equal(t, http.StatusOK, moved.Code, moved.Body.String())
	auditLine()
	assert.Equal(t, fleet.credB, fleet.stored(t, "zone-a").Proxmox.CredentialID)
}
