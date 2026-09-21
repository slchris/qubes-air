package repository

import (
	"context"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/require"
)

func TestPurgeClaimWithdrawsIdentityAtomically(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	zone := createTestZone(t, NewZoneRepository(db))
	qubes := NewQubeRepository(db)
	id := claimTestQube(t, qubes, zone.ID, "remote-purge", models.QubeStatusSuspended)
	certs := NewAgentCertRepository(db)
	tokens := NewBootstrapTokenRepository(db)
	ctx := context.Background()
	cert := &AgentCert{Fingerprint: "old", QubeID: id, SubjectCN: "agent-remote-purge", IssuedAt: time.Now()}
	require.NoError(t, certs.Register(ctx, cert))
	token, err := tokens.Issue(ctx, id, "remote-purge", time.Hour)
	require.NoError(t, err)
	require.NoError(t, qubes.ClaimPurge(ctx, id, []models.QubeStatus{models.QubeStatusSuspended}))
	_, err = certs.Authorize(ctx, "old")
	require.ErrorIs(t, err, ErrCertRevoked)
	_, err = tokens.Redeem(ctx, token, time.Now())
	require.ErrorIs(t, err, ErrBootstrapTokenRejected)
	_, err = tokens.Issue(ctx, id, "remote-purge", time.Hour)
	require.ErrorContains(t, err, "purge requested")
	cert.Fingerprint = "new"
	require.ErrorContains(t, certs.Register(ctx, cert), "purge requested")
}
