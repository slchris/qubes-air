package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bootstrapper the server builds must take its first-contact pins from the
// token store. Wired without them it refuses every qube with a message naming
// the missing provider; wired with them it refuses a qube that has no live
// token and dials one that has.
func TestBootstrapperTakesFirstContactPinsFromTheTokenStore(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Orchestrator.AgentListen = "127.0.0.1:1"
	dbCfg := database.DefaultConfig()
	dbCfg.DSN = filepath.Join(t.TempDir(), "console.db")
	db, err := database.New(dbCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	kr, err := cfg.Keyring()
	require.NoError(t, err)
	tokens := repository.NewBootstrapTokenRepository(db)
	certs := repository.NewAgentCertRepository(db)
	issuer := newCertIssuer(cfg, repository.NewCredentialRepository(db, kr), certs, tokens)
	boot := buildBootstrapper(cfg, issuer, tokens, certs)

	qube := &models.Qube{ID: "qube-1", Name: "remote-dev", IPAddress: "127.0.0.1"}
	res := boot.Bootstrap(context.Background(), qube)
	assert.Equal(t, service.BootstrapNotConfigured, res.Status)
	assert.Contains(t, res.Reason, "no unredeemed, unexpired bootstrap token",
		"the wired bootstrapper must consult the token store, not report a missing provider")

	_, err = tokens.Issue(context.Background(), qube.ID, qube.Name, time.Hour)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res = boot.Bootstrap(ctx, qube)
	assert.Equal(t, service.BootstrapUnreachable, res.Status,
		"with a pinned token outstanding the bootstrapper dials (and finds nothing listening): %s", res.Reason)
}
