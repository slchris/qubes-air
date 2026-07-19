// Package service provides business logic for qube management.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/transport"
)

// Qube service errors.
var (
	ErrQubeNotFound     = errors.New("qube not found")
	ErrQubeNotStopped   = errors.New("qube must be stopped")
	ErrZoneDisconnected = errors.New("zone is disconnected")
	ErrInvalidQubeType  = errors.New("invalid qube type")
	// ErrOrchestration wraps a failure that occurred while triggering the real
	// infrastructure action (terraform suspend/resume). When this is returned
	// the DB status is left unchanged.
	ErrOrchestration = errors.New("orchestration action failed")
	// ErrUnreachable wraps a failure to reach a remote qube over the gRPC
	// transport (cross-machine qrexec). The health-check call did not complete.
	ErrUnreachable = errors.New("remote qube unreachable")
)

// pingService is the qrexec service used by CheckReachable to probe a remote
// qube's reachability over the tunnel. [TODO] provide qubesair.Ping on the
// remote (a trivial qrexec service that returns "pong").
const pingService = "qubesair.Ping"

// QubeService defines qube business logic operations.
type QubeService interface { //nolint:dupl
	Create(ctx context.Context, req *models.QubeCreateRequest) (*models.Qube, error)
	GetByID(ctx context.Context, id string) (*models.Qube, error)
	List(ctx context.Context, opts repository.QubeListOptions) ([]*models.Qube, error)
	Update(ctx context.Context, id string, req *models.QubeUpdateRequest) (*models.Qube, error)
	Delete(ctx context.Context, id string) error
	Start(ctx context.Context, id string) (*models.Qube, error)
	Stop(ctx context.Context, id string) (*models.Qube, error)
	// CheckReachable probes a remote qube over the gRPC transport (cross-machine
	// qrexec health check). Returns the probe response on success.
	CheckReachable(ctx context.Context, id string) (string, error)
}

// QubeServiceImpl implements QubeService.
type QubeServiceImpl struct {
	qubeRepo repository.QubeRepository
	zoneRepo repository.ZoneRepository
	// executor triggers real infrastructure actions (terraform suspend/resume).
	// It is never nil: when no executor is injected a NoopExecutor is used so
	// existing behaviour and tests are preserved.
	executor orchestrator.Executor
	// transport forwards cross-machine qrexec calls to remote qubes over the
	// gRPC tunnel. Never nil: defaults to NoopTransport (CheckReachable then
	// fails loudly with "no transport configured").
	transport transport.Transport
}

// QubeServiceOption customizes a QubeService at construction. Options keep
// NewQubeService backward compatible: existing callers that pass only the two
// repositories still compile and get a NoopExecutor.
type QubeServiceOption func(*QubeServiceImpl)

// WithExecutor injects the orchestration executor used by Start/Stop. Passing a
// nil executor is ignored (the NoopExecutor default is kept).
func WithExecutor(exec orchestrator.Executor) QubeServiceOption {
	return func(s *QubeServiceImpl) {
		if exec != nil {
			s.executor = exec
		}
	}
}

// WithTransport injects the cross-machine gRPC transport used by CheckReachable.
// Passing nil is ignored (the NoopTransport default is kept).
func WithTransport(t transport.Transport) QubeServiceOption {
	return func(s *QubeServiceImpl) {
		if t != nil {
			s.transport = t
		}
	}
}

// NewQubeService creates a new QubeService. By default it uses a NoopExecutor
// (no infrastructure calls); pass WithExecutor to wire a real orchestrator.
func NewQubeService(
	qubeRepo repository.QubeRepository,
	zoneRepo repository.ZoneRepository,
	opts ...QubeServiceOption,
) QubeService {
	s := &QubeServiceImpl{
		qubeRepo:  qubeRepo,
		zoneRepo:  zoneRepo,
		executor:  orchestrator.NewNoopExecutor(),
		transport: transport.NoopTransport{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Create creates a new qube.
func (s *QubeServiceImpl) Create(ctx context.Context, req *models.QubeCreateRequest) (*models.Qube, error) {
	if err := s.validateQubeCreateRequest(ctx, req); err != nil {
		return nil, err
	}

	qube := buildNewQube(req)
	applyDefaultSpec(qube)

	if err := s.qubeRepo.Create(ctx, qube); err != nil {
		return nil, err
	}

	return qube, nil
}

// validateQubeCreateRequest validates qube creation request.
func (s *QubeServiceImpl) validateQubeCreateRequest(ctx context.Context, req *models.QubeCreateRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("qube name is required")
	}

	if !req.Type.IsValid() {
		return ErrInvalidQubeType
	}

	// Zone is optional - only validate if provided
	if req.ZoneID != "" {
		if _, err := s.zoneRepo.GetByID(ctx, req.ZoneID); err != nil {
			return ErrZoneNotFound
		}
	}

	return nil
}

// buildNewQube constructs a new Qube from the request.
func buildNewQube(req *models.QubeCreateRequest) *models.Qube {
	return &models.Qube{
		ID:        uuid.New().String(),
		Name:      strings.TrimSpace(req.Name),
		Type:      req.Type,
		ZoneID:    req.ZoneID,
		Status:    models.QubeStatusStopped,
		Spec:      req.Spec,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// applyDefaultSpec applies type-based default specifications.
func applyDefaultSpec(qube *models.Qube) {
	if qube.Spec.VCPU == 0 {
		qube.Spec.VCPU = getDefaultVCPU(qube.Type)
	}
	if qube.Spec.Memory == 0 {
		qube.Spec.Memory = getDefaultMemory(qube.Type)
	}
	if qube.Spec.Disk == 0 {
		qube.Spec.Disk = getDefaultDisk(qube.Type)
	}
}

// getDefaultVCPU returns default vCPU count by qube type.
func getDefaultVCPU(qubeType models.QubeType) int {
	switch qubeType {
	case models.QubeTypeApp:
		return 2
	case models.QubeTypeWork:
		return 4
	case models.QubeTypeGPU:
		return 8
	default:
		return 2
	}
}

// getDefaultMemory returns default memory in MB by qube type.
func getDefaultMemory(qubeType models.QubeType) int {
	switch qubeType {
	case models.QubeTypeApp:
		return 2048
	case models.QubeTypeWork:
		return 4096
	case models.QubeTypeGPU:
		return 16384
	default:
		return 2048
	}
}

// getDefaultDisk returns default disk in GB by qube type.
func getDefaultDisk(qubeType models.QubeType) int {
	switch qubeType {
	case models.QubeTypeApp:
		return 20
	case models.QubeTypeWork:
		return 50
	case models.QubeTypeGPU:
		return 100
	default:
		return 20
	}
}

// GetByID retrieves a qube by ID.
func (s *QubeServiceImpl) GetByID(ctx context.Context, id string) (*models.Qube, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}
	return qube, nil
}

// List retrieves all qubes with optional filtering.
func (s *QubeServiceImpl) List(ctx context.Context, opts repository.QubeListOptions) ([]*models.Qube, error) {
	return s.qubeRepo.List(ctx, opts)
}

// Update updates an existing qube.
func (s *QubeServiceImpl) Update(ctx context.Context, id string, req *models.QubeUpdateRequest) (*models.Qube, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}

	applyQubeUpdates(qube, req)
	qube.UpdatedAt = time.Now()

	if err := s.qubeRepo.Update(ctx, qube); err != nil {
		return nil, err
	}

	return qube, nil
}

// applyQubeUpdates applies update request fields to qube.
func applyQubeUpdates(qube *models.Qube, req *models.QubeUpdateRequest) {
	if req.Name != nil {
		qube.Name = strings.TrimSpace(*req.Name)
	}
	if req.Spec != nil {
		qube.Spec = *req.Spec
	}
}

// Delete removes a qube.
func (s *QubeServiceImpl) Delete(ctx context.Context, id string) error {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return ErrQubeNotFound
	}

	if qube.Status == models.QubeStatusRunning {
		return ErrQubeNotStopped
	}

	return s.qubeRepo.Delete(ctx, id)
}

// Start starts (resumes) a qube.
//
// Order matters: preconditions are checked first, then the orchestrator rebuilds
// the compute instance (terraform resume), and only if that SUCCEEDS is the DB
// status flipped to running. If orchestration fails the DB status is left
// untouched and an error is returned — we never report "running" for a qube the
// infrastructure did not actually bring up.
func (s *QubeServiceImpl) Start(ctx context.Context, id string) (*models.Qube, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}

	if err := s.verifyZoneConnected(ctx, qube.ZoneID); err != nil {
		return nil, err
	}

	// 1) Trigger the real infrastructure action first.
	if err := s.executor.Resume(ctx, qube.Name); err != nil {
		return nil, fmt.Errorf("%w: resume %q: %v", ErrOrchestration, qube.Name, err)
	}

	// 2) Only after resume succeeds do we record the new state.
	if err := s.qubeRepo.UpdateStatus(ctx, id, models.QubeStatusRunning); err != nil {
		return nil, err
	}

	return s.qubeRepo.GetByID(ctx, id)
}

// verifyZoneConnected checks if the zone is connected.
func (s *QubeServiceImpl) verifyZoneConnected(ctx context.Context, zoneID string) error {
	zone, err := s.zoneRepo.GetByID(ctx, zoneID)
	if err != nil {
		return ErrZoneNotFound
	}

	if zone.Status != "connected" {
		return ErrZoneDisconnected
	}

	return nil
}

// Stop stops (suspends) a qube.
//
// Same "act first, record second" discipline as Start: the orchestrator releases
// the compute instance (terraform suspend) while keeping the data disk; only if
// that succeeds is the DB status updated. The resulting status is Suspended —
// distinct from Stopped — to reflect that compute was released but data is
// preserved and the qube can be resumed. If orchestration fails the DB status is
// left unchanged.
func (s *QubeServiceImpl) Stop(ctx context.Context, id string) (*models.Qube, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}

	// 1) Trigger the real infrastructure action first.
	if err := s.executor.Suspend(ctx, qube.Name); err != nil {
		return nil, fmt.Errorf("%w: suspend %q: %v", ErrOrchestration, qube.Name, err)
	}

	// 2) Only after suspend succeeds do we record the new state.
	if err := s.qubeRepo.UpdateStatus(ctx, id, models.QubeStatusSuspended); err != nil {
		return nil, err
	}

	return s.qubeRepo.GetByID(ctx, id)
}

// CheckReachable probes a remote qube's reachability over the gRPC transport: it
// forwards a qrexec health-check call (qubesair.Ping) to the qube's name across
// the tunnel and returns the response. This is a genuine cross-machine qrexec
// call — distinct from Start/Stop, which drive terraform. It does not change any
// state.
//
// With the default NoopTransport it returns ErrUnreachable wrapping
// "no transport configured", so the feature fails loudly until a real transport
// is wired (transport.enabled=true).
func (s *QubeServiceImpl) CheckReachable(ctx context.Context, id string) (string, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return "", ErrQubeNotFound
	}
	if err := s.verifyZoneConnected(ctx, qube.ZoneID); err != nil {
		return "", err
	}

	// Forward a qrexec health check to the remote qube over the tunnel. The
	// transport validates the target/service name and only carries the frame;
	// both dom0s authorize the call.
	resp, err := s.transport.Call(ctx, qube.Name, pingService, nil)
	if err != nil {
		return "", fmt.Errorf("%w: ping %q: %v", ErrUnreachable, qube.Name, err)
	}
	return strings.TrimSpace(string(resp)), nil
}
