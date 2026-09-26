package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestZoneCreateIsGatedByTheWiredRegistry — initDependencies must build the zone
// service from the same provider registry the executor dispatches through. A
// zone service wired to anything else (no registry, or a separate list) would
// either refuse Proxmox or go back to accepting types whose first provision job
// fails with provider.ErrNoAdapter.
func TestZoneCreateIsGatedByTheWiredRegistry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Mode = gin.TestMode
	cfg.Database.DSN = filepath.Join(t.TempDir(), "console.db")

	deps, err := initDependencies(cfg)
	require.NoError(t, err)
	t.Cleanup(deps.Close)
	router := setupRouter(cfg, deps)

	createZone := func(zoneType models.ZoneType) int {
		body, err := json.Marshal(models.ZoneCreateRequest{Name: "zone-" + string(zoneType), Type: zoneType})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/zones", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	for _, zoneType := range []models.ZoneType{models.ZoneTypeGCP, models.ZoneTypeAWS, models.ZoneTypeAzure} {
		assert.Equal(t, http.StatusUnprocessableEntity, createZone(zoneType), "zone type %q has no adapter", zoneType)
	}
	assert.Equal(t, http.StatusCreated, createZone(models.ZoneTypeProxmox))
}

// TestZoneCreateChecksCredentialReferencesAsWired — initDependencies must give
// the zone service the operator's view of the credential store. Without it a
// zone naming any credential is refused (fail closed); with the wrong view a
// zone could name a console row.
func TestZoneCreateChecksCredentialReferencesAsWired(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Mode = gin.TestMode
	cfg.Database.DSN = filepath.Join(t.TempDir(), "console.db")

	deps, err := initDependencies(cfg)
	require.NoError(t, err)
	t.Cleanup(deps.Close)
	router := setupRouter(cfg, deps)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	cred := post("/api/v1/credentials", `{"name":"pve","type":"proxmox","secret":"root@pam!ci=abc"}`)
	require.Equal(t, http.StatusCreated, cred.Code, cred.Body.String())
	var created struct {
		Credential models.Credential `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(cred.Body.Bytes(), &created))

	zone := func(credentialID string) int {
		return post("/api/v1/zones", `{"name":"z-`+credentialID+`","type":"proxmox","config":{"proxmox":{"credential_id":"`+credentialID+`"}}}`).Code
	}
	assert.Equal(t, http.StatusCreated, zone(created.Credential.ID))
	assert.Equal(t, http.StatusUnprocessableEntity, zone("no-such-credential"))
}
