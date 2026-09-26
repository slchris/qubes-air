package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// credentialAPI is the credentials handler over a real encrypted store that
// already holds the console's CA, one qube's data key and its migration
// marker, and one operator credential.
type credentialAPI struct {
	router     *gin.Engine
	repo       *repository.CredentialRepository
	consoleIDs []string
	operatorID string
}

func newCredentialAPI(t *testing.T, closeDB bool) credentialAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	f, err := os.CreateTemp("", "credential-handler-*.db")
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
	repo := repository.NewCredentialRepository(db, kr)
	api := credentialAPI{repo: repo}

	_, err = service.NewCertIssuer(repo, repository.NewAgentCertRepository(db), "", "", service.AgentPackage{}).CA(ctx)
	require.NoError(t, err)
	keys := service.NewDataKeyManager(repo)
	_, err = keys.EnsureDataKey(ctx, "q1")
	require.NoError(t, err)
	require.NoError(t, keys.MarkMigrationPending(ctx, "q1"))
	all, err := repo.List(ctx)
	require.NoError(t, err)
	for _, c := range all {
		api.consoleIDs = append(api.consoleIDs, c.ID)
	}
	require.Len(t, api.consoleIDs, 4, "CA cert, CA key, data key, marker")

	op, err := repo.Create(ctx, models.CredentialCreateRequest{Name: "pve-prod", Type: "proxmox", SecretValue: "root@pam!ci=abc"})
	require.NoError(t, err)
	api.operatorID = op.ID

	if closeDB {
		require.NoError(t, db.Close())
	}
	api.router = gin.New()
	NewCredentialHandler(service.NewCredentialService(repo)).RegisterRoutes(api.router.Group("/api/v1"))
	return api
}

func (a credentialAPI) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	a.router.ServeHTTP(w, req)
	return w
}

func TestCredentialListOmitsConsoleRows(t *testing.T) {
	api := newCredentialAPI(t, false)
	w := api.do(http.MethodGet, "/api/v1/credentials", "")
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Credentials []models.Credential `json:"credentials"`
		Total       int                 `json:"total"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Credentials, 1)
	assert.Equal(t, api.operatorID, resp.Credentials[0].ID)
	assert.Equal(t, 1, resp.Total, "total counts only what is listed")
	for _, name := range []string{"qubes-air-", "pki"} {
		assert.NotContains(t, w.Body.String(), name)
	}
}

// TestCredentialConsoleRowAnswersLikeAMissingID — reading, updating or deleting
// a console row's ID gets exactly the response a nonexistent ID gets, and the
// row is left alone.
func TestCredentialConsoleRowAnswersLikeAMissingID(t *testing.T) {
	api := newCredentialAPI(t, false)
	ctx := context.Background()
	requests := []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPut, `{"name":"renamed","description":"changed","secret":"overwritten"}`},
		{http.MethodDelete, ""},
	}
	for _, req := range requests {
		missing := api.do(req.method, "/api/v1/credentials/no-such-id", req.body)
		require.Equal(t, http.StatusNotFound, missing.Code, "%s of a missing ID", req.method)
		for _, id := range api.consoleIDs {
			before, err := api.repo.GetByID(ctx, id)
			require.NoError(t, err)
			secret, err := api.repo.GetSecret(ctx, id)
			require.NoError(t, err)

			w := api.do(req.method, "/api/v1/credentials/"+id, req.body)
			assert.Equal(t, missing.Code, w.Code, "%s of a console row", req.method)
			assert.Equal(t, missing.Body.String(), w.Body.String(), "%s of a console row must answer like a missing ID", req.method)

			after, err := api.repo.GetByID(ctx, id)
			require.NoError(t, err)
			require.NotNil(t, after, "a console row must survive %s", req.method)
			assert.Equal(t, before.Name, after.Name)
			assert.Equal(t, before.Description, after.Description)
			assert.Equal(t, before.UpdatedAt, after.UpdatedAt)
			stillSecret, err := api.repo.GetSecret(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, secret, stillSecret)
		}
	}
}

func TestCredentialCreateAndRenameRefuseTheConsoleNamespace(t *testing.T) {
	api := newCredentialAPI(t, false)
	ctx := context.Background()
	before, err := api.repo.List(ctx)
	require.NoError(t, err)

	for _, body := range []string{
		`{"name":"qubes-air-ca-key","type":"other","secret":"attacker"}`,
		`{"name":"QUBES-AIR-LUKS-KEY-q2","type":"other","secret":"attacker"}`,
		`{"name":"ordinary","type":"pki","secret":"attacker"}`,
	} {
		w := api.do(http.MethodPost, "/api/v1/credentials", body)
		assert.Equal(t, http.StatusForbidden, w.Code, body)
		assert.Contains(t, w.Body.String(), "reserved")
		assert.NotContains(t, w.Body.String(), "attacker", "the refusal must not echo the secret")
	}
	after, err := api.repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "a refused create must store nothing")

	w := api.do(http.MethodPut, "/api/v1/credentials/"+api.operatorID, `{"name":"qubes-air-ca-cert"}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
	row, err := api.repo.GetByID(ctx, api.operatorID)
	require.NoError(t, err)
	assert.Equal(t, "pve-prod", row.Name)
}

func TestCredentialOperatorRowLifecycle(t *testing.T) {
	api := newCredentialAPI(t, false)

	w := api.do(http.MethodPost, "/api/v1/credentials", `{"name":"pve-lab","type":"proxmox","secret":"root@pam!ci=one"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Credential models.Credential `json:"credential"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.Credential.ID
	assert.NotContains(t, w.Body.String(), "root@pam", "the secret is never returned")

	assert.Equal(t, http.StatusOK, api.do(http.MethodGet, "/api/v1/credentials/"+id, "").Code)
	w = api.do(http.MethodPut, "/api/v1/credentials/"+id, `{"name":"pve-lab-2"}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "pve-lab-2")
	assert.Equal(t, http.StatusOK, api.do(http.MethodDelete, "/api/v1/credentials/"+id, "").Code)

	// Updating or deleting an ID that is gone is a 404, not a server error.
	assert.Equal(t, http.StatusNotFound, api.do(http.MethodDelete, "/api/v1/credentials/"+id, "").Code)
	assert.Equal(t, http.StatusNotFound, api.do(http.MethodPut, "/api/v1/credentials/"+id, `{"name":"x"}`).Code)
}

func TestCredentialRequestErrors(t *testing.T) {
	api := newCredentialAPI(t, false)
	assert.Equal(t, http.StatusBadRequest, api.do(http.MethodPost, "/api/v1/credentials", `{"name":"no-secret","type":"other"}`).Code)
	assert.Equal(t, http.StatusBadRequest, api.do(http.MethodPut, "/api/v1/credentials/"+api.operatorID, `{"name":`).Code)
}

// TestCredentialStorageFailureIsGeneric — a storage error is answered with a
// generic 500; its text stays in the server log.
func TestCredentialStorageFailureIsGeneric(t *testing.T) {
	api := newCredentialAPI(t, true)
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/credentials", ""},
		{http.MethodGet, "/api/v1/credentials/" + api.operatorID, ""},
		{http.MethodPost, "/api/v1/credentials", `{"name":"n","type":"other","secret":"s"}`},
		{http.MethodPut, "/api/v1/credentials/" + api.operatorID, `{"name":"n"}`},
		{http.MethodDelete, "/api/v1/credentials/" + api.operatorID, ""},
	} {
		w := api.do(req.method, req.path, req.body)
		assert.Equal(t, http.StatusInternalServerError, w.Code, "%s %s", req.method, req.path)
		assert.JSONEq(t, `{"error":"Internal Server Error"}`, w.Body.String(), "%s %s", req.method, req.path)
	}
}
