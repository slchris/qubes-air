package main

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/handler"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const credentialRoute = "/api/v1/credentials/:id"

// leakMarker is the secret value the refused requests carry. It must never
// reach the audit output. (Named and spliced into the bodies so the literal
// is not a JSON "secret" field that secret scanners report.)
const leakMarker = "attacker-SECRET-31c7"

// credentialStore is a real encrypted store holding the console's CA key row
// and one operator credential, as the credentials API sees them in production.
type credentialStore struct {
	repo       *repository.CredentialRepository
	caKeyID    string
	operatorID string
	// zones and qubes share the store's database, for tests that wire the
	// zone service over it.
	zones repository.ZoneRepository
	qubes repository.QubeRepository
}

func newCredentialStore(t *testing.T) credentialStore {
	t.Helper()
	ctx := context.Background()
	f, err := os.CreateTemp("", "credentials-api-*.db")
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
	kr, err := keyring.NewSingle([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	store := credentialStore{
		repo:  repository.NewCredentialRepository(db, kr),
		zones: repository.NewZoneRepository(db),
		qubes: repository.NewQubeRepository(db),
	}

	_, err = service.NewCertIssuer(store.repo, repository.NewAgentCertRepository(db), "", "", service.AgentPackage{}).CA(ctx)
	require.NoError(t, err)
	all, err := store.repo.List(ctx)
	require.NoError(t, err)
	for _, c := range all {
		if c.Name == "qubes-air-ca-key" {
			store.caKeyID = c.ID
		}
	}
	require.NotEmpty(t, store.caKeyID)
	op, err := store.repo.Create(ctx, models.CredentialCreateRequest{Name: "pve-prod", Type: "proxmox", SecretValue: "v"})
	require.NoError(t, err)
	store.operatorID = op.ID
	return store
}

func (s credentialStore) api(t *testing.T) (*gin.Engine, func() map[string]any) {
	t.Helper()
	r, buf, _ := auditedAPIWith(t, nil, func(v1 *gin.RouterGroup) {
		handler.NewCredentialHandler(service.NewCredentialService(s.repo)).RegisterRoutes(v1)
	})
	return r, func() map[string]any {
		line := onlyAuditLine(t, buf)
		assertNoCredentialMaterial(t, buf, leakMarker)
		buf.Reset()
		return line
	}
}

func (s credentialStore) requireCAKeyIntact(t *testing.T) {
	t.Helper()
	row, err := s.repo.GetByID(context.Background(), s.caKeyID)
	require.NoError(t, err)
	require.NotNil(t, row, "the CA key row must survive")
	assert.Equal(t, "qubes-air-ca-key", row.Name)
}

// TestCredentialsAPIHidesConsoleRowsBehindTheChain drives the production
// middleware chain with a fleet-wide control token. A console row's ID
// answers exactly like a missing ID, yet the audit trail records the attempt
// as denied rather than as an ordinary miss; a create into the console's
// namespace is refused and recorded as denied too.
func TestCredentialsAPIHidesConsoleRowsBehindTheChain(t *testing.T) {
	store := newCredentialStore(t)
	r, auditLine := store.api(t)
	admin := bearer(adminTokenValue)

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		body := `{"name":"renamed","secret":"` + leakMarker + `"}`
		if method == http.MethodDelete {
			body = ""
		}
		missing := apiRequest(r, method, "/api/v1/credentials/no-such-id", body, admin)
		require.Equal(t, http.StatusNotFound, missing.Code)
		assertAuditFields(t, auditLine(), missing, map[string]any{
			"outcome": audit.OutcomeClientError, "route": credentialRoute, "object": "no-such-id",
		})

		w := apiRequest(r, method, "/api/v1/credentials/"+store.caKeyID, body, admin)
		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, missing.Body.String(), w.Body.String(), "%s must not reveal the row exists", method)
		assertAuditFields(t, auditLine(), w, map[string]any{
			"outcome": audit.OutcomeDenied, "authenticated": true, "subject": "api_token",
			"route": credentialRoute, "object": store.caKeyID,
		})
		store.requireCAKeyIntact(t)
	}

	w := apiRequest(r, http.MethodGet, "/api/v1/credentials/"+store.caKeyID, "", admin)
	assert.Equal(t, http.StatusNotFound, w.Code)

	w = apiRequest(r, http.MethodPost, "/api/v1/credentials",
		`{"name":"qubes-air-ca-key","type":"other","secret":"`+leakMarker+`"}`, admin)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assertAuditFields(t, auditLine(), w, map[string]any{
		"outcome": audit.OutcomeDenied, "route": "/api/v1/credentials", "object": "",
	})
}

// TestCredentialsAPIIsFleetOnly — a zone-restricted token cannot reach the
// credentials API at all, whatever the row: RequireZones refuses the fleet
// endpoint before the handler runs. A read-only token cannot change a row.
func TestCredentialsAPIIsFleetOnly(t *testing.T) {
	store := newCredentialStore(t)
	r, auditLine := store.api(t)

	for _, id := range []string{store.operatorID, store.caKeyID} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			body := ""
			if method == http.MethodPut {
				body = `{"name":"renamed"}`
			}
			w := apiRequest(r, method, "/api/v1/credentials/"+id, body, bearer(zoneTokenValue))
			assert.Equal(t, http.StatusForbidden, w.Code, "zone token %s", method)
			if method != http.MethodGet {
				assertAuditFields(t, auditLine(), w, map[string]any{
					"outcome": audit.OutcomeDenied, "subject": "zone-a-control", "zone_scope": "zone-a",
				})
			}
		}
		w := apiRequest(r, http.MethodDelete, "/api/v1/credentials/"+id, "", bearer(auditorTokenValue))
		assert.Equal(t, http.StatusForbidden, w.Code, "read-only token")
		assertAuditFields(t, auditLine(), w, map[string]any{"outcome": audit.OutcomeDenied, "subject": "auditor"})
	}
	w := apiRequest(r, http.MethodGet, "/api/v1/credentials", "", bearer(zoneTokenValue))
	assert.Equal(t, http.StatusForbidden, w.Code, "zone token list")

	store.requireCAKeyIntact(t)
	row, err := store.repo.GetByID(context.Background(), store.operatorID)
	require.NoError(t, err)
	assert.Equal(t, "pve-prod", row.Name, "refused requests must not change the operator row")
}
