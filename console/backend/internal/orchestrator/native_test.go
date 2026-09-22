package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdapter records calls and returns canned results. Unset funcs default to
// a successful no-op so a test names only the behavior it exercises.
type fakeAdapter struct {
	ensureStorage   func(ctx context.Context, in provider.Infra) (provider.Infra, error)
	ensureCompute   func(ctx context.Context, in provider.Infra) (provider.Infra, error)
	stopCompute     func(ctx context.Context, in provider.Infra) error
	destroyStorage  func(ctx context.Context, in provider.Infra) error
	verifyDestroyed func(context.Context, provider.Infra) error
	describe        func(ctx context.Context, in provider.Infra) (provider.Observed, error)
}

func (f *fakeAdapter) EnsureStorage(_ context.Context, _ *models.Qube, _ *models.Zone, in provider.Infra) (provider.Infra, error) {
	if f.ensureStorage != nil {
		return f.ensureStorage(context.Background(), in)
	}
	return in, nil
}

func (f *fakeAdapter) EnsureCompute(_ context.Context, _ *models.Qube, _ *models.Zone, in provider.Infra) (provider.Infra, error) {
	if f.ensureCompute != nil {
		return f.ensureCompute(context.Background(), in)
	}
	return in, nil
}

func (f *fakeAdapter) StopCompute(_ context.Context, _ *models.Qube, in provider.Infra) error {
	if f.stopCompute != nil {
		return f.stopCompute(context.Background(), in)
	}
	return nil
}

func (f *fakeAdapter) DestroyStorage(_ context.Context, _ *models.Qube, in provider.Infra) error {
	if f.destroyStorage != nil {
		return f.destroyStorage(context.Background(), in)
	}
	return nil
}

func (f *fakeAdapter) Describe(_ context.Context, _ *models.Qube, in provider.Infra) (provider.Observed, error) {
	if f.describe != nil {
		return f.describe(context.Background(), in)
	}
	return provider.Observed{}, nil
}

// memStore is an in-memory InfraStore.
type memStore struct {
	rows map[string]provider.Infra
}

func (m *memStore) Get(_ context.Context, qubeID string) (*provider.Infra, error) {
	inf, ok := m.rows[qubeID]
	if !ok {
		return nil, nil
	}
	cp := inf
	return &cp, nil
}

func (m *memStore) Save(_ context.Context, inf *provider.Infra) error {
	if m.rows == nil {
		m.rows = make(map[string]provider.Infra)
	}
	m.rows[inf.QubeID] = *inf
	return nil
}

func (m *memStore) Delete(_ context.Context, qubeID string) error {
	delete(m.rows, qubeID)
	return nil
}

// fakeResolver returns a fixed qube/zone pair.
type fakeResolver struct {
	qube *models.Qube
	zone *models.Zone
	err  error
}

func (f fakeResolver) Resolve(context.Context, string) (*models.Qube, *models.Zone, error) {
	return f.qube, f.zone, f.err
}

func testQube() *models.Qube {
	return &models.Qube{ID: "q1", Name: "remote-1", ZoneID: "z1"}
}

func testZone() *models.Zone {
	return &models.Zone{ID: "z1", Name: "pve", Type: models.ZoneTypeProxmox}
}

func newNativeTestExecutor(t *testing.T, ad provider.Adapter) (*NativeExecutor, *memStore) {
	t.Helper()
	reg := provider.NewRegistry()
	require.NoError(t, reg.Register(models.ZoneTypeProxmox,
		func(context.Context, *models.Zone) (provider.Adapter, error) { return ad, nil }))
	store := &memStore{}
	return NewNativeExecutor(reg, fakeResolver{qube: testQube(), zone: testZone()}, store), store
}

func TestNativeExecutor_ProvisionOrdersAndRecords(t *testing.T) {
	var order []string
	ad := &fakeAdapter{}
	ad.ensureStorage = func(_ context.Context, in provider.Infra) (provider.Infra, error) {
		order = append(order, "storage")
		in.StorageVMID = 100
		in.DataVolume = "ceph-pve:vm-100-disk-0"
		return in, nil
	}
	ad.ensureCompute = func(_ context.Context, in provider.Infra) (provider.Infra, error) {
		order = append(order, "compute")
		assert.Equal(t, 100, in.StorageVMID, "compute must see the recorded storage id")
		in.ComputeVMID = 101
		return in, nil
	}
	exec, store := newNativeTestExecutor(t, ad)

	require.NoError(t, exec.Provision(context.Background(), "remote-1"))
	assert.Equal(t, []string{"storage", "compute"}, order)

	inf, err := store.Get(context.Background(), "q1")
	require.NoError(t, err)
	require.NotNil(t, inf)
	assert.Equal(t, 100, inf.StorageVMID)
	assert.Equal(t, 101, inf.ComputeVMID)
	assert.Equal(t, "ceph-pve:vm-100-disk-0", inf.DataVolume)
	assert.True(t, inf.Protected, "a new data disk must be protected")
}

func TestNativeExecutor_ProvisionStorageFailureLeavesNoRecord(t *testing.T) {
	ad := &fakeAdapter{}
	ad.ensureStorage = func(context.Context, provider.Infra) (provider.Infra, error) {
		return provider.Infra{}, errors.New("cluster unreachable")
	}
	exec, store := newNativeTestExecutor(t, ad)

	err := exec.Provision(context.Background(), "remote-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure storage")
	assert.Empty(t, store.rows)
}

func TestNativeExecutor_ProvisionComputeFailureKeepsStorageRecord(t *testing.T) {
	ad := &fakeAdapter{}
	ad.ensureStorage = func(_ context.Context, in provider.Infra) (provider.Infra, error) {
		in.StorageVMID = 100
		in.DataVolume = "ceph-pve:vm-100-disk-0"
		return in, nil
	}
	ad.ensureCompute = func(_ context.Context, in provider.Infra) (provider.Infra, error) {
		return provider.Infra{}, errors.New("clone failed")
	}
	exec, store := newNativeTestExecutor(t, ad)

	err := exec.Provision(context.Background(), "remote-1")
	require.Error(t, err)

	inf, err := store.Get(context.Background(), "q1")
	require.NoError(t, err)
	require.NotNil(t, inf, "the created data disk must stay recorded so it is adoptable")
	assert.Equal(t, 100, inf.StorageVMID)
	assert.Zero(t, inf.ComputeVMID)
}

func TestNativeExecutor_ResumeRecoversInterruptedProvision(t *testing.T) {
	called := false
	ad := &fakeAdapter{ensureStorage: func(_ context.Context, in provider.Infra) (provider.Infra, error) {
		called = true
		in.StorageVMID, in.DataVolume = 105, "ceph:vm-105-disk-0"
		return in, nil
	}}
	exec, _ := newNativeTestExecutor(t, ad)
	require.NoError(t, exec.Resume(context.Background(), "remote-1"))
	assert.True(t, called, "retry must recover storage before compute")
}

func TestNativeExecutor_ResumeBuildsCompute(t *testing.T) {
	ad := &fakeAdapter{}
	ad.ensureCompute = func(_ context.Context, in provider.Infra) (provider.Infra, error) {
		in.ComputeVMID = 101
		return in, nil
	}
	exec, store := newNativeTestExecutor(t, ad)
	require.NoError(t, store.Save(context.Background(), &provider.Infra{
		QubeID: "q1", Provider: "proxmox", StorageVMID: 100, Protected: true,
	}))

	require.NoError(t, exec.Resume(context.Background(), "remote-1"))
	inf, _ := store.Get(context.Background(), "q1")
	require.NotNil(t, inf)
	assert.Equal(t, 101, inf.ComputeVMID)
	assert.Equal(t, 100, inf.StorageVMID, "resume must not touch the persistent disk")
}

func TestNativeExecutor_SuspendClearsCompute(t *testing.T) {
	called := false
	ad := &fakeAdapter{}
	ad.stopCompute = func(_ context.Context, in provider.Infra) error {
		called = true
		assert.Equal(t, 101, in.ComputeVMID)
		return nil
	}
	exec, store := newNativeTestExecutor(t, ad)
	require.NoError(t, store.Save(context.Background(), &provider.Infra{
		QubeID: "q1", Provider: "proxmox", StorageVMID: 100, ComputeVMID: 101, Protected: true,
	}))

	require.NoError(t, exec.Suspend(context.Background(), "remote-1"))
	assert.True(t, called)
	inf, _ := store.Get(context.Background(), "q1")
	require.NotNil(t, inf)
	assert.Zero(t, inf.ComputeVMID)
	assert.Equal(t, "suspended", inf.ObservedState)
	assert.Equal(t, 100, inf.StorageVMID)
}

func TestNativeExecutor_DestroyRefusesProtected(t *testing.T) {
	destroyed := false
	ad := &fakeAdapter{}
	ad.destroyStorage = func(context.Context, provider.Infra) error {
		destroyed = true
		return nil
	}
	exec, store := newNativeTestExecutor(t, ad)
	require.NoError(t, store.Save(context.Background(), &provider.Infra{
		QubeID: "q1", Provider: "proxmox", StorageVMID: 100, Protected: true,
	}))

	err := exec.Destroy(context.Background(), "remote-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInfraProtected)
	assert.False(t, destroyed, "protected data must not be destroyed")
	_, _ = store.Get(context.Background(), "q1") // row untouched
}

func TestNativeExecutor_DestroyUnprotectedPurges(t *testing.T) {
	var order []string
	ad := &fakeAdapter{}
	ad.stopCompute = func(context.Context, provider.Infra) error {
		order = append(order, "stop")
		return nil
	}
	ad.destroyStorage = func(context.Context, provider.Infra) error {
		order = append(order, "storage")
		return nil
	}
	exec, store := newNativeTestExecutor(t, ad)
	require.NoError(t, store.Save(context.Background(), &provider.Infra{
		QubeID: "q1", Provider: "proxmox", StorageVMID: 100, ComputeVMID: 101, Protected: false,
	}))

	require.NoError(t, exec.Destroy(context.Background(), "remote-1"))
	assert.Equal(t, []string{"stop", "storage"}, order)
	inf, _ := store.Get(context.Background(), "q1")
	assert.Nil(t, inf, "the record must be removed after a purge")
}

func TestNativeExecutor_DestroyWithoutInfraIsNoop(t *testing.T) {
	exec, _ := newNativeTestExecutor(t, &fakeAdapter{})
	require.NoError(t, exec.Destroy(context.Background(), "remote-1"))
}

func TestNativeExecutor_StatusAndAddress(t *testing.T) {
	ad := &fakeAdapter{}
	ad.describe = func(context.Context, provider.Infra) (provider.Observed, error) {
		return provider.Observed{State: "running", IPAddress: "10.0.0.5"}, nil
	}
	exec, _ := newNativeTestExecutor(t, ad)

	state, err := exec.Status(context.Background(), "remote-1")
	require.NoError(t, err)
	assert.Equal(t, "running", state)

	addr, err := exec.Address(context.Background(), "remote-1")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.5", addr)
}

func TestNativeExecutor_RejectsInvalidName(t *testing.T) {
	exec, _ := newNativeTestExecutor(t, &fakeAdapter{})
	err := exec.Provision(context.Background(), "bad name;rm -rf /")
	require.Error(t, err)

	var invalid *ErrInvalidQubeName
	assert.ErrorAs(t, err, &invalid)
}

func TestNativeExecutor_NoAdapterFailsLoudly(t *testing.T) {
	reg := provider.NewRegistry()
	exec := NewNativeExecutor(reg, fakeResolver{qube: testQube(), zone: testZone()}, &memStore{})

	err := exec.Provision(context.Background(), "remote-1")
	require.Error(t, err)

	var noAdapter *provider.ErrNoAdapter
	assert.ErrorAs(t, err, &noAdapter)
}

// fakeProbe fails its first failTimes calls, then succeeds. It records the last
// qube it saw so a test can assert the executor refreshed the address first.
type fakeProbe struct {
	failTimes int
	calls     int
	last      *models.Qube
}

func (p *fakeProbe) ProbeReachable(_ context.Context, q *models.Qube) error {
	p.calls++
	p.last = q
	if p.calls <= p.failTimes {
		return errors.New("connection refused")
	}
	return nil
}

func TestNativeExecutor_ProvisionWaitsForReachable(t *testing.T) {
	ad := &fakeAdapter{}
	ad.describe = func(context.Context, provider.Infra) (provider.Observed, error) {
		return provider.Observed{IPAddress: "10.31.0.70"}, nil
	}
	probe := &fakeProbe{failTimes: 2}
	exec, _ := newNativeTestExecutor(t, ad)
	exec.WithReachability(probe)
	exec.probeInterval = time.Millisecond
	exec.probeTimeout = time.Second

	require.NoError(t, exec.Provision(context.Background(), "remote-1"))
	assert.Equal(t, 3, probe.calls, "two failures then a success")
	require.NotNil(t, probe.last)
	assert.Equal(t, "10.31.0.70", probe.last.IPAddress, "the probe must see the address from Describe")
}

func TestNativeExecutor_ProvisionFailsWhenAgentNeverReachable(t *testing.T) {
	probe := &fakeProbe{failTimes: 1 << 30}
	exec, _ := newNativeTestExecutor(t, &fakeAdapter{})
	exec.WithReachability(probe)
	exec.probeInterval = time.Millisecond
	exec.probeTimeout = 20 * time.Millisecond

	err := exec.Provision(context.Background(), "remote-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not become reachable")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestNativeExecutor_NoProbeSkipsGate(t *testing.T) {
	exec, _ := newNativeTestExecutor(t, &fakeAdapter{})
	require.NoError(t, exec.Provision(context.Background(), "remote-1"))
}

func (f *fakeAdapter) VerifyDestroyed(ctx context.Context, _ *models.Qube, in provider.Infra) error {
	if f.verifyDestroyed != nil {
		return f.verifyDestroyed(ctx, in)
	}
	return nil
}

func TestDestroyRetainsRecordUntilIndependentVerification(t *testing.T) {
	failure := errors.New("data volume remains")
	ad := &fakeAdapter{verifyDestroyed: func(context.Context, provider.Infra) error { return failure }}
	exec, store := newNativeTestExecutor(t, ad)
	require.NoError(t, store.Save(context.Background(), &provider.Infra{QubeID: "q1", StorageVMID: 105, DataVolume: "ceph:vm-105-disk-0"}))
	require.ErrorIs(t, exec.Destroy(context.Background(), "remote-1"), failure)
	in, err := store.Get(context.Background(), "q1")
	require.NoError(t, err)
	require.NotNil(t, in)
	ad.verifyDestroyed = nil
	require.NoError(t, exec.Destroy(context.Background(), "remote-1"))
	in, err = store.Get(context.Background(), "q1")
	require.NoError(t, err)
	require.Nil(t, in)
}

type failingInfraStore struct {
	*memStore
	saveError   bool
	deleteError bool
}

func (s *failingInfraStore) Save(ctx context.Context, in *provider.Infra) error {
	if s.saveError {
		return errors.New("injected save failure")
	}
	return s.memStore.Save(ctx, in)
}
func (s *failingInfraStore) Delete(ctx context.Context, id string) error {
	if s.deleteError {
		return errors.New("injected delete failure")
	}
	return s.memStore.Delete(ctx, id)
}

func TestDestroyFailureStagesRetainIdentityForRetry(t *testing.T) {
	for _, stage := range []string{"stop", "save", "storage", "verify", "delete"} {
		t.Run(stage, func(t *testing.T) {
			boom := errors.New("injected " + stage)
			ad := &fakeAdapter{}
			exec, base := newNativeTestExecutor(t, ad)
			store := &failingInfraStore{memStore: base}
			exec.store = store
			require.NoError(t, base.Save(context.Background(), &provider.Infra{
				QubeID: "q1", ComputeVMID: 106, StorageVMID: 105, DataVolume: "ceph:vm-105-disk-0",
			}))
			switch stage {
			case "stop":
				ad.stopCompute = func(context.Context, provider.Infra) error { return boom }
			case "save":
				store.saveError = true
			case "storage":
				ad.destroyStorage = func(context.Context, provider.Infra) error { return boom }
			case "verify":
				ad.verifyDestroyed = func(context.Context, provider.Infra) error { return boom }
			case "delete":
				store.deleteError = true
			}
			require.Error(t, exec.Destroy(context.Background(), "remote-1"))
			in, err := store.Get(context.Background(), "q1")
			require.NoError(t, err)
			require.NotNil(t, in)
			assert.Equal(t, 105, in.StorageVMID)
			assert.Equal(t, "ceph:vm-105-disk-0", in.DataVolume)
			if stage == "stop" || stage == "save" {
				assert.Equal(t, 106, in.ComputeVMID)
			}
			ad.stopCompute, ad.destroyStorage, ad.verifyDestroyed = nil, nil, nil
			store.saveError, store.deleteError = false, false
			require.NoError(t, exec.Destroy(context.Background(), "remote-1"))
		})
	}
}
