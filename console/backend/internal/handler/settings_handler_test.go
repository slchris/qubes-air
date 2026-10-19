package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingsFixture is a settings handler over a real database and a session
// store, so a test can see both what was stored and what sessions now get.
type settingsFixture struct {
	router   *gin.Engine
	repo     *repository.SettingsRepository
	sessions *middleware.SessionStore
	db       *database.DB
}

func newSettingsHandlerFixture(t *testing.T, ttl time.Duration) settingsFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "settings.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := repository.NewSettingsRepository(db)
	sessions := middleware.NewSessionStore(ttl)
	r := gin.New()
	NewSettingsHandler(service.NewSettingsService(repo), sessions).RegisterRoutes(r.Group("/api/v1"))
	return settingsFixture{router: r, repo: repo, sessions: sessions, db: db}
}

func settingsBody(security string) string {
	return `{"general":{"timezone":"UTC","language":"en","theme":"system"},` +
		`"notifications":{"email":false,"webhook":false,"webhookUrl":""},"security":` + security + `}`
}

func (f settingsFixture) put(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// newSessionLifetime reports the lifetime the store gives a session issued now.
func (f settingsFixture) newSessionLifetime(t *testing.T) time.Duration {
	t.Helper()
	sess, err := f.sessions.Create("operator", middleware.ScopeControl, nil)
	require.NoError(t, err)
	return sess.Expires.Sub(sess.Created)
}

func (f settingsFixture) storedTimeout(t *testing.T) int {
	t.Helper()
	stored, err := f.repo.Get(context.Background())
	require.NoError(t, err)
	return stored.Security.SessionTimeout
}

// TestSettingsUpdateAppliesTheSessionTimeout — a saved timeout governs sessions
// at once: new ones get it, issued ones are cut down to it.
func TestSettingsUpdateAppliesTheSessionTimeout(t *testing.T) {
	f := newSettingsHandlerFixture(t, time.Hour)
	issued, err := f.sessions.Create("operator", middleware.ScopeControl, nil)
	require.NoError(t, err)

	w := f.put(settingsBody(`{"sessionTimeout":5,"twoFactorEnabled":false}`))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	current, ok := f.sessions.Get(issued.ID)
	require.True(t, ok)
	assert.Equal(t, 5*time.Minute, current.Expires.Sub(current.Created), "an issued session is shortened")
	assert.Equal(t, 5*time.Minute, f.newSessionLifetime(t))
	assert.Equal(t, 5, f.storedTimeout(t))
}

// TestSettingsUpdateNeverExtendsIssuedSessions — raising the timeout applies to
// new sessions only.
func TestSettingsUpdateNeverExtendsIssuedSessions(t *testing.T) {
	f := newSettingsHandlerFixture(t, 30*time.Minute)
	issued, err := f.sessions.Create("operator", middleware.ScopeControl, nil)
	require.NoError(t, err)

	w := f.put(settingsBody(`{"sessionTimeout":1440}`))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	current, ok := f.sessions.Get(issued.ID)
	require.True(t, ok)
	assert.Equal(t, issued.Expires, current.Expires)
	assert.Equal(t, 24*time.Hour, f.newSessionLifetime(t))
}

// TestSettingsUpdateRejectsUnsafeTimeouts — out-of-range and non-numeric
// timeouts are client errors that change neither the database nor sessions.
func TestSettingsUpdateRejectsUnsafeTimeouts(t *testing.T) {
	cases := map[string]string{
		"zero":          `{"sessionTimeout":0}`,
		"below minimum": `{"sessionTimeout":4}`,
		"above maximum": `{"sessionTimeout":1441}`,
		"negative":      `{"sessionTimeout":-30}`,
		"missing":       `{}`,
		"string":        `{"sessionTimeout":"30"}`,
		"fraction":      `{"sessionTimeout":30.5}`,
		"huge":          `{"sessionTimeout":1e30}`,
	}
	for name, security := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSettingsHandlerFixture(t, 45*time.Minute)
			w := f.put(settingsBody(security))
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

			var body ErrorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.NotEmpty(t, body.Message, "the refusal must say why")
			assert.Equal(t, 45*time.Minute, f.newSessionLifetime(t), "a refused save must not touch sessions")
			assert.Equal(t, service.DefaultSessionTimeoutMinutes, f.storedTimeout(t), "a refused save must not be stored")
		})
	}
}

func TestSettingsUpdateRangeMessageNamesTheBounds(t *testing.T) {
	f := newSettingsHandlerFixture(t, 0)
	w := f.put(settingsBody(`{"sessionTimeout":1}`))
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "outside 5-1440")
}

// TestSettingsUpdateRejectsUnimplementedSettings — email notifications and 2FA
// do nothing, so switching them on is refused rather than stored.
func TestSettingsUpdateRejectsUnimplementedSettings(t *testing.T) {
	cases := map[string]string{
		"two-factor": `{"general":{},"notifications":{"email":false},"security":{"sessionTimeout":30,"twoFactorEnabled":true}}`,
		"email":      `{"general":{},"notifications":{"email":true},"security":{"sessionTimeout":30}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSettingsHandlerFixture(t, 45*time.Minute)
			w := f.put(body)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "setting is not implemented")
			assert.Equal(t, 45*time.Minute, f.newSessionLifetime(t))
		})
	}
}

// overlapProbe stands in for the settings service. Its first Update is held
// open until a second Update arrives or holdOpen passes, and it records whether
// two Updates were ever inside at once. Under the handler's lock a second save
// cannot reach Update while the first is held, so it never overlaps; without
// the lock it reaches Update during the hold every time.
type overlapProbe struct {
	holdOpen      time.Duration
	firstEntered  chan struct{}
	secondEntered chan struct{}

	mu      sync.Mutex
	calls   int
	inside  int
	overlap bool
	stored  []int
}

func newOverlapProbe(holdOpen time.Duration) *overlapProbe {
	return &overlapProbe{
		holdOpen:      holdOpen,
		firstEntered:  make(chan struct{}),
		secondEntered: make(chan struct{}),
	}
}

func (p *overlapProbe) Get(context.Context) (*models.Settings, error) {
	return &models.Settings{}, nil
}

func (p *overlapProbe) Update(_ context.Context, settings *models.Settings) error {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.inside++
	if p.inside > 1 {
		p.overlap = true
	}
	p.mu.Unlock()

	switch call {
	case 1:
		close(p.firstEntered)
		select {
		case <-p.secondEntered:
		case <-time.After(p.holdOpen):
		}
	case 2:
		close(p.secondEntered)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.stored = append(p.stored, settings.Security.SessionTimeout)
	p.inside--
	return nil
}

// TestSettingsUpdateSerializesStoreAndApply — two saves must not interleave
// between "stored" and "applied", or sessions can end on a timeout that is not
// the one stored last. The probe holds the first save inside Update while the
// second is sent: under the handler's lock the second waits, so the saves never
// overlap, run in order, and sessions end on the last stored value. Removing the
// lock makes the second save enter Update during the hold, and this fails.
func TestSettingsUpdateSerializesStoreAndApply(t *testing.T) {
	gin.SetMode(gin.TestMode)
	probe := newOverlapProbe(time.Second)
	sessions := middleware.NewSessionStore(time.Hour)
	r := gin.New()
	(&SettingsHandler{svc: probe, sessions: sessions}).RegisterRoutes(r.Group("/api/v1"))
	f := settingsFixture{router: r, sessions: sessions}

	var wg sync.WaitGroup
	save := func(minutes int) {
		defer wg.Done()
		w := f.put(settingsBody(fmt.Sprintf(`{"sessionTimeout":%d}`, minutes)))
		if w.Code != http.StatusOK {
			t.Errorf("save %d minutes: %d %s", minutes, w.Code, w.Body.String())
		}
	}
	wg.Add(2)
	go save(10)
	select {
	case <-probe.firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first save never reached the settings service")
	}
	go save(20)
	wg.Wait()

	probe.mu.Lock()
	defer probe.mu.Unlock()
	require.False(t, probe.overlap, "two saves were inside Update at once: store-then-apply is not serialized")
	require.Equal(t, []int{10, 20}, probe.stored)
	assert.Equal(t, 20*time.Minute, f.newSessionLifetime(t), "sessions must end on the timeout stored last")
}

// TestSettingsGetReportsTheEffectiveTimeout — a timeout an older release stored
// out of range reads as the default sessions actually use.
func TestSettingsGetReportsTheEffectiveTimeout(t *testing.T) {
	f := newSettingsHandlerFixture(t, 0)
	settings, err := f.repo.Get(context.Background())
	require.NoError(t, err)
	settings.Security.SessionTimeout = 2
	settings.Notifications.Email = true
	require.NoError(t, f.repo.Update(context.Background(), settings))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"sessionTimeout":30`)
	assert.Contains(t, w.Body.String(), `"email":false`)
}

// TestSettingsFailedSaveLeavesStoredSettingsAndSessionsUntouched — a storage
// failure must not apply the timeout, and it must not leave the first groups
// written while the last one failed. The trigger fails only the last group, so
// the assertion on the earlier groups is what proves the update is atomic.
func TestSettingsFailedSaveLeavesStoredSettingsAndSessionsUntouched(t *testing.T) {
	f := newSettingsHandlerFixture(t, 30*time.Minute)
	issued, err := f.sessions.Create("operator", middleware.ScopeControl, nil)
	require.NoError(t, err)
	_, err = f.db.DB().Exec(
		`CREATE TRIGGER reject_security BEFORE INSERT ON settings
		 WHEN NEW.key = 'security'
		 BEGIN SELECT RAISE(FAIL, 'injected failure'); END`)
	require.NoError(t, err)

	w := f.put(settingsBody(`{"sessionTimeout":5}`))
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())

	current, ok := f.sessions.Get(issued.ID)
	require.True(t, ok)
	assert.Equal(t, issued.Expires, current.Expires, "a failed save must not shorten an issued session")
	assert.Equal(t, 30*time.Minute, f.newSessionLifetime(t), "a failed save must not change the lifetime of new sessions")

	stored, err := f.repo.Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "light", stored.General.Theme, "the first group must not be committed")
	assert.True(t, stored.Notifications.Email, "the second group must not be committed")
	assert.Equal(t, 30, stored.Security.SessionTimeout)
}

// newWebhookSettingsHandlerFixture is the same fixture as
// newSettingsHandlerFixture, but with the credential store and allowlist the
// production server wires. It returns the raw credential repository so a test
// can prove the destination was encrypted rather than merely echoed.
func newWebhookSettingsHandlerFixture(t *testing.T) (settingsFixture, *repository.CredentialRepository) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "settings.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	kr, err := keyring.NewSingle([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	credentials := repository.NewCredentialRepository(db, kr)
	repo := repository.NewSettingsRepository(db)
	sessions := middleware.NewSessionStore(30 * time.Minute)
	svc := service.NewSettingsService(repo).
		WithWebhookCredentials(credentials).
		WithWebhookAllowedHosts([]string{"hooks.example.test"})
	r := gin.New()
	NewSettingsHandler(svc, sessions).RegisterRoutes(r.Group("/api/v1"))
	return settingsFixture{router: r, repo: repo, sessions: sessions, db: db}, credentials
}

func webhookSettingsBody(target string, enabled bool) string {
	return fmt.Sprintf(`{"general":{"timezone":"UTC","language":"en","theme":"system"},`+
		`"notifications":{"email":false,"webhook":%t,"webhookUrl":%q},`+
		`"security":{"sessionTimeout":30}}`, enabled, target)
}

func TestSettingsWebhookDestinationIsEncryptedAndRedacted(t *testing.T) {
	f, credentials := newWebhookSettingsHandlerFixture(t)
	const target = "https://hooks.example.test/v1/alerts"
	w := f.put(webhookSettingsBody(target, true))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "hooks.example.test", "the destination must never be returned")
	require.Contains(t, w.Body.String(), `"webhookConfigured":true`)

	stored, configured, err := credentials.GetAlertWebhookTarget(context.Background())
	require.NoError(t, err)
	require.True(t, configured)
	require.Equal(t, target, stored)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "hooks.example.test")
	require.Contains(t, recorder.Body.String(), `"webhookConfigured":true`)
}

func TestSettingsWebhookRejectsDestinationOutsideAllowlist(t *testing.T) {
	f, credentials := newWebhookSettingsHandlerFixture(t)
	w := f.put(webhookSettingsBody("https://evil.example.test/alerts", true))
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "webhook_allowed_hosts")

	_, configured, err := credentials.GetAlertWebhookTarget(context.Background())
	require.NoError(t, err)
	require.False(t, configured, "a rejected destination must not be stored")

	stored, err := f.repo.Get(context.Background())
	require.NoError(t, err)
	require.False(t, stored.Notifications.Webhook)
}

func TestSettingsWebhookDisableRemovesEncryptedDestination(t *testing.T) {
	f, credentials := newWebhookSettingsHandlerFixture(t)
	require.Equal(t, http.StatusOK, f.put(webhookSettingsBody("https://hooks.example.test/alerts", true)).Code)
	_, configured, err := credentials.GetAlertWebhookTarget(context.Background())
	require.NoError(t, err)
	require.True(t, configured)

	w := f.put(webhookSettingsBody("", false))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	_, configured, err = credentials.GetAlertWebhookTarget(context.Background())
	require.NoError(t, err)
	require.False(t, configured)
}
