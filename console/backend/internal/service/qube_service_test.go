package service

import (
	"context"
	"os"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupQubeTestServices(t *testing.T) (ZoneService, QubeService, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-service-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()

	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)

	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	return zoneSvc, qubeSvc, cleanup
}

func createConnectedZone(t *testing.T, zoneSvc ZoneService) *models.Zone {
	t.Helper()

	ctx := context.Background()
	req := &models.ZoneCreateRequest{
		Name: "Test Zone",
		Type: models.ZoneTypeProxmox,
	}
	zone, err := zoneSvc.Create(ctx, req)
	require.NoError(t, err)

	zone, err = zoneSvc.Connect(ctx, zone.ID)
	require.NoError(t, err)

	return zone
}

func TestQubeService_Create(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "test-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}

	qubeOp, err := qubeSvc.Create(ctx, req)
	assert.NoError(t, err)
	assert.NotEmpty(t, qubeOp.Qube.ID)
	assert.Equal(t, req.Name, qubeOp.Qube.Name)
	// Create now provisions: with the default Noop executor the inline run
	// succeeds immediately, so the qube settles on running rather than stopped.
	assert.Equal(t, models.QubeStatusRunning, qubeOp.Qube.Status)
}

func TestQubeService_Create_WithDefaultSpecs(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "default-spec-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}

	qubeOp, err := qubeSvc.Create(ctx, req)
	assert.NoError(t, err)
	assert.Greater(t, qubeOp.Qube.Spec.VCPU, 0)
	assert.Greater(t, qubeOp.Qube.Spec.Memory, 0)
}

func TestQubeService_Create_InvalidZone(t *testing.T) {
	_, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.QubeCreateRequest{
		Name:   "invalid-zone-qube",
		Type:   models.QubeTypeApp,
		ZoneID: "nonexistent-zone",
	}

	_, err := qubeSvc.Create(ctx, req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrZoneNotFound)
}

func TestQubeService_Create_InvalidType(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "Invalid Type Qube",
		Type:   "invalid-type",
		ZoneID: zone.ID,
	}

	_, err := qubeSvc.Create(ctx, req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidQubeType)
}

func TestQubeService_GetByID(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "get-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	createdOp, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)

	qube, err := qubeSvc.GetByID(ctx, createdOp.Qube.ID)
	assert.NoError(t, err)
	assert.Equal(t, createdOp.Qube.ID, qube.ID)
}

func TestQubeService_GetByID_NotFound(t *testing.T) {
	_, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()

	_, err := qubeSvc.GetByID(ctx, "nonexistent-id")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrQubeNotFound)
}

func TestQubeService_List(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	for i := range 3 {
		req := &models.QubeCreateRequest{
			Name:   "qube-" + string(rune('a'+i)),
			Type:   models.QubeTypeApp,
			ZoneID: zone.ID,
		}
		_, err := qubeSvc.Create(ctx, req)
		require.NoError(t, err)
	}

	qubes, err := qubeSvc.List(ctx, repository.DefaultQubeListOptions())
	assert.NoError(t, err)
	assert.Len(t, qubes, 3)
}

func TestQubeService_Update(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	createReq := &models.QubeCreateRequest{
		Name:   "Original",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	createdOp, err := qubeSvc.Create(ctx, createReq)
	require.NoError(t, err)

	newName := "Updated"
	updateReq := &models.QubeUpdateRequest{
		Name: &newName,
	}
	updated, err := qubeSvc.Update(ctx, createdOp.Qube.ID, updateReq)
	assert.NoError(t, err)
	assert.Equal(t, "Updated", updated.Name)
}

func TestQubeService_Delete(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "to-delete",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	createdOp, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)

	err = qubeSvc.Delete(ctx, createdOp.Qube.ID)
	assert.NoError(t, err)

	// Delete RELEASES rather than destroys: the compute instance goes away but
	// the data disk, and the row describing it, are kept. The row must survive —
	// it is what keeps the qube in the rendered terraform variables, and dropping
	// it while the prevent_destroy storage VM is still in state would wedge every
	// subsequent apply for every qube.
	after, err := qubeSvc.GetByID(ctx, createdOp.Qube.ID)
	require.NoError(t, err, "a released qube must still exist")
	assert.Equal(t, models.QubeStatusReleased, after.Status)
}

func TestQubeService_Start(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		// Name must be terraform-safe: it maps to a terraform -target address.
		Name:   "start-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	createdOp, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)

	// Create now provisions, so the qube is already running. Starting a running
	// qube is refused — suspend it first to get a startable state.
	_, err = qubeSvc.Stop(ctx, createdOp.Qube.ID)
	require.NoError(t, err)

	qubeOp, err := qubeSvc.Start(ctx, createdOp.Qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusRunning, qubeOp.Qube.Status)
}

func TestQubeService_Stop(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "stop-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	// Create already leaves the qube running — no explicit Start needed.
	createdOp, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)
	require.Equal(t, models.QubeStatusRunning, createdOp.Qube.Status)

	// Stop suspends (releases compute, keeps the data disk), so the recorded
	// status is Suspended rather than Stopped.
	qubeOp, err := qubeSvc.Stop(ctx, createdOp.Qube.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.QubeStatusSuspended, qubeOp.Qube.Status)
}

// TestCreateAppliesEncryptDataFleetDefault covers the tri-state: an omitted
// encrypt_data inherits the fleet default, while an explicit value always wins —
// so a fleet can default to encrypted and still let one qube opt out (and the
// reverse). Without the pointer, an omitted field would be indistinguishable
// from false and "default on" could never work.
func TestCreateAppliesEncryptDataFleetDefault(t *testing.T) {
	ptr := func(b bool) *bool { return &b }
	cases := []struct {
		name       string
		fleet      bool
		reqEncrypt *bool
		want       bool
	}{
		{"default-on, unset -> encrypted", true, nil, true},
		{"default-off, unset -> plaintext", false, nil, false},
		{"default-on, explicit false -> opt out", true, ptr(false), false},
		{"default-off, explicit true -> opt in", false, ptr(true), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp, err := os.CreateTemp("", "enc-default-*.db")
			require.NoError(t, err)
			tmp.Close()
			defer os.Remove(tmp.Name())
			cfg := database.DefaultConfig()
			cfg.DSN = tmp.Name()
			db, err := database.New(cfg)
			require.NoError(t, err)
			defer db.Close()

			zoneRepo := repository.NewZoneRepository(db)
			qubeRepo := repository.NewQubeRepository(db)
			zoneSvc := NewZoneService(zoneRepo, qubeRepo)
			qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithEncryptDataDefault(tc.fleet))

			ctx := context.Background()
			zone := createConnectedZone(t, zoneSvc)
			op, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
				Name: "enc-default-qube", Type: models.QubeTypeApp, ZoneID: zone.ID,
				Spec: models.QubeSpec{EncryptData: tc.reqEncrypt},
			})
			require.NoError(t, err)
			require.NotNil(t, op.Qube.Spec.EncryptData, "resolved value must be concrete, never nil")
			assert.Equal(t, tc.want, op.Qube.Spec.EncryptsData())
		})
	}
}

// TestComputeDestroyingAction pins the contract the IP-clearing depends on: the
// actions that tear down the compute VM (and thus invalidate its address) are
// exactly suspend, release and destroy — never provision or resume.
func TestComputeDestroyingAction(t *testing.T) {
	for _, a := range []orchestrator.Action{
		orchestrator.ActionSuspend, orchestrator.ActionRelease, orchestrator.ActionDestroy,
	} {
		assert.True(t, ComputeDestroyingAction(a), "%q tears down the compute VM", a)
	}
	for _, a := range []orchestrator.Action{
		orchestrator.ActionProvision, orchestrator.ActionResume,
	} {
		assert.False(t, ComputeDestroyingAction(a), "%q keeps the compute VM", a)
	}
}

// TestQubeService_SuspendClearsStaleIP is the regression guard for a bug found on
// hardware: a resume rebuilds the compute VM with a new MAC and a new DHCP lease,
// so the IP recorded while the qube ran is stale the instant it is suspended. The
// health monitor only re-reads an address when the stored one is empty, so if
// suspend/release does not clear it the console dials the dead address forever
// and the resumed qube never becomes reachable. Suspend must clear it.
func TestQubeService_SuspendClearsStaleIP(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "qube-ip-clear-*.db")
	require.NoError(t, err)
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)
	defer db.Close()

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo)

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	created, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "ip-clear-qube", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)
	require.Equal(t, models.QubeStatusRunning, created.Qube.Status)

	// Stand in for the health monitor having learned an address while it ran.
	require.NoError(t, qubeRepo.UpdateIPAddress(ctx, created.Qube.ID, "10.31.0.150"))

	op, err := qubeSvc.Stop(ctx, created.Qube.ID)
	require.NoError(t, err)
	require.Equal(t, models.QubeStatusSuspended, op.Qube.Status)
	assert.Empty(t, op.Qube.IPAddress, "suspend must clear the stale IP in the returned qube")

	reloaded, err := qubeRepo.GetByID(ctx, created.Qube.ID)
	require.NoError(t, err)
	assert.Empty(t, reloaded.IPAddress, "suspend must clear the stale IP in the database")
}

func TestQubeService_Start_ZoneDisconnected(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "disconnected-zone-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	createdOp, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)

	_, err = zoneSvc.Disconnect(ctx, zone.ID)
	require.NoError(t, err)

	_, err = qubeSvc.Start(ctx, createdOp.Qube.ID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrZoneDisconnected)
}
