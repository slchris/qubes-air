package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupWithTransport builds a QubeService with an injected transport plus a
// connected zone and a qube in it, returning the qube id.
func setupWithTransport(t *testing.T, xport transport.Transport) (QubeService, string, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-reachable-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithTransport(xport))

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)
	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "reach-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}
	return qubeSvc, createdOp.Qube.ID, cleanup
}

func TestCheckReachable_OK(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(target, service string, _ []byte) ([]byte, error) {
			// The service must forward the qube name as target and the ping service.
			if target != "reach-qube" || service != pingService {
				return nil, errors.New("unexpected call")
			}
			return []byte("pong\n"), nil
		},
	}
	svc, id, cleanup := setupWithTransport(t, fake)
	defer cleanup()

	resp, err := svc.CheckReachable(context.Background(), id)
	assert.NoError(t, err)
	assert.Equal(t, "pong", resp) // trimmed
	assert.Equal(t, 1, fake.CallCount())
	assert.Equal(t, "reach-qube", fake.Calls[0].Target)
	assert.Equal(t, pingService, fake.Calls[0].Service)
}

func TestCheckReachable_TransportError(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			return nil, errors.New("tunnel down")
		},
	}
	svc, id, cleanup := setupWithTransport(t, fake)
	defer cleanup()

	_, err := svc.CheckReachable(context.Background(), id)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
}

func TestCheckReachable_NoTransportConfigured(t *testing.T) {
	// Default service uses NoopTransport → fails loudly with ErrUnreachable.
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)
	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "noop-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	_, err = qubeSvc.CheckReachable(ctx, createdOp.Qube.ID)
	assert.ErrorIs(t, err, ErrUnreachable)
}

func TestCheckReachable_NotFound(t *testing.T) {
	svc, _, cleanup := setupWithTransport(t, &transport.FakeTransport{})
	defer cleanup()

	_, err := svc.CheckReachable(context.Background(), "does-not-exist")
	assert.ErrorIs(t, err, ErrQubeNotFound)
}
