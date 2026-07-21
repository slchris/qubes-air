// Package service provides business logic for qube management.
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
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

	// ErrInvalidQubeName means the name cannot be used as a terraform map key
	// or -target address: only alphanumerics, '-', '_' and '.', starting with
	// an alphanumeric, at most 64 characters.
	ErrInvalidQubeName = errors.New("invalid qube name")

	// ErrPlacement means no cluster node could take the qube. This is a hard
	// failure by design: Proxmox would accept an overcommitted placement and let
	// the node thrash instead.
	ErrPlacement = errors.New("no node available for this qube")
	// ErrUnreachable wraps a failure to reach a remote qube over the gRPC
	// transport (cross-machine qrexec). The health-check call did not complete.
	ErrUnreachable = errors.New("remote qube unreachable")
)

// pingService is the qrexec service used by CheckReachable to probe a remote
// qube's reachability over the tunnel. [TODO] provide qubesair.Ping on the
// remote (a trivial qrexec service that returns "pong").
const pingService = "qubesair.Ping"

// QubeService defines qube business logic operations.
type QubeService interface {
	// Create records the qube and enqueues a provision job. The infrastructure
	// does not exist when this returns — poll the job.
	Create(ctx context.Context, req *models.QubeCreateRequest) (*Operation, error)
	GetByID(ctx context.Context, id string) (*models.Qube, error)
	List(ctx context.Context, opts repository.QubeListOptions) ([]*models.Qube, error)
	Update(ctx context.Context, id string, req *models.QubeUpdateRequest) (*models.Qube, error)
	Delete(ctx context.Context, id string) error
	// Start and Stop are asynchronous: they claim the qube into a transient
	// status, enqueue a terraform job and return immediately. A real apply takes
	// minutes, far beyond any HTTP write deadline.
	Start(ctx context.Context, id string) (*Operation, error)
	Stop(ctx context.Context, id string) (*Operation, error)
	// CheckReachable probes a remote qube over the gRPC transport (cross-machine
	// qrexec health check). Returns the probe response on success.
	CheckReachable(ctx context.Context, id string) (string, error)
	// ProbeAgent probes ONE qube's agent and records what it found. It is the
	// single answer to "is this agent alive": the on-demand endpoint, the
	// post-provision settle loop and the periodic reconciler all come through
	// here, so they cannot disagree.
	ProbeAgent(ctx context.Context, qube *models.Qube, phase AgentProbePhase) AgentProbeResult
}

// AgentProbePhase says how an INCONCLUSIVE probe should be recorded. It changes
// only the stored health, never the probe itself or the reason attached to it.
type AgentProbePhase string

// Agent probe phases.
const (
	// AgentProbeSteady records what was observed. This is the normal case: the
	// qube has been up long enough that "no answer" means the agent is broken.
	AgentProbeSteady AgentProbePhase = "steady"
	// AgentProbeSettling records a failure as "starting" rather than
	// "unreachable" because the qube is still inside its post-boot budget.
	//
	// Not a softer failure — a different fact. cloud-init installs the agent
	// after the VM reports its address, so a just-provisioned qube legitimately
	// refuses connections for a while. Recording that as unreachable would flag
	// every healthy qube and train operators to disregard the field.
	AgentProbeSettling AgentProbePhase = "settling"
)

// QubeServiceImpl implements QubeService.
type QubeServiceImpl struct {
	qubeRepo repository.QubeRepository
	zoneRepo repository.ZoneRepository
	// executor triggers real infrastructure actions (terraform suspend/resume).
	// It is never nil: when no executor is injected a NoopExecutor is used so
	// existing behavior and tests are preserved.
	executor orchestrator.Executor
	// transport forwards cross-machine qrexec calls to remote qubes over the
	// gRPC tunnel. Never nil: defaults to NoopTransport (CheckReachable then
	// fails loudly with "no transport configured").
	//
	// It is pinned to one configured RemoteEndpoint, so it can only ever reach
	// that one remote. It is kept as the FALLBACK for a console with no CA
	// wired; prober is what can actually ask "is THIS qube's agent alive".
	transport transport.Transport
	// prober dials each qube's own address to check its agent. Nil falls back
	// to transport, which cannot address an arbitrary qube — a degradation, so
	// it is logged when it happens rather than passing for a real answer.
	prober *AgentProber
	// issuer mints and registers the agent's client certificate. Nil disables
	// issuance, in which case a qube is created without an agent identity and
	// its agent cannot authenticate.
	issuer *CertIssuer
	// placer chooses which cluster node a qube runs on. Nil disables automatic
	// scheduling, in which case placement falls back to the zone default.
	placer PlacementDecider
	// submitter queues terraform work. When nil the service runs the executor
	// inline, which preserves the previous synchronous behavior for tests and
	// for deployments with no orchestration configured.
	submitter JobSubmitter
	// renewals reports certificates that are failing to renew, so a probe can
	// carry that warning alongside its own result. Nil simply omits it.
	renewals RenewalWatch
	// encryptDataDefault is the fleet default for a create request that does not
	// specify encrypt_data. false (the zero value) preserves the historical
	// behavior — plaintext unless asked — so a console that never sets it is
	// unchanged. A create can always override it explicitly either way.
	encryptDataDefault bool
}

// RenewalWatch reports an outstanding certificate-renewal problem for a qube.
// Implemented by *CertRenewalMonitor.
type RenewalWatch interface {
	RenewalWarning(qubeID string) string
}

// JobSubmitter queues an infrastructure operation and returns the job that will
// carry it out. Implemented by orchestrator.Runner.
type JobSubmitter interface {
	Submit(ctx context.Context, qubeID, qubeName string, action orchestrator.Action) (*orchestrator.Job, error)
}

// Operation is what a mutating qube endpoint returns: the qube as it stands now
// (already in a transient status) plus the id of the job doing the real work.
type Operation struct {
	Qube  *models.Qube `json:"qube"`
	JobID string       `json:"job_id,omitempty"`
}

// QubeServiceOption customizes a QubeService at construction. Options keep
// NewQubeService backward compatible: existing callers that pass only the two
// repositories still compile and get a NoopExecutor.
type QubeServiceOption func(*QubeServiceImpl)

// WithEncryptDataDefault sets the fleet default for a create request that does
// not specify encrypt_data. Off preserves the plaintext-unless-asked behavior.
func WithEncryptDataDefault(on bool) QubeServiceOption {
	return func(s *QubeServiceImpl) { s.encryptDataDefault = on }
}

// WithExecutor injects the orchestration executor used by Start/Stop. Passing a
// nil executor is ignored (the NoopExecutor default is kept).
func WithExecutor(exec orchestrator.Executor) QubeServiceOption {
	return func(s *QubeServiceImpl) {
		if exec != nil {
			s.executor = exec
		}
	}
}

// WithCertIssuer enables agent certificate issuance at qube creation.
func WithCertIssuer(i *CertIssuer) QubeServiceOption {
	return func(s *QubeServiceImpl) { s.issuer = i }
}

// WithPlacementDecider enables automatic node selection. Without it a qube is
// placed on the zone's default node.
func WithPlacementDecider(p PlacementDecider) QubeServiceOption {
	return func(s *QubeServiceImpl) { s.placer = p }
}

// WithJobSubmitter makes orchestration asynchronous by queueing work instead of
// running it inline. Without it the service falls back to running the executor
// synchronously, which keeps tests and unconfigured deployments working.
func WithJobSubmitter(js JobSubmitter) QubeServiceOption {
	return func(s *QubeServiceImpl) { s.submitter = js }
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

// WithAgentProber enables per-qube agent probing. Without it agent health falls
// back to the single global transport, which is pinned to one endpoint and so
// cannot answer the question for an arbitrary qube.
func WithAgentProber(p *AgentProber) QubeServiceOption {
	return func(s *QubeServiceImpl) { s.prober = p }
}

// WithRenewalWatch makes certificate-renewal failures visible in agent health.
//
// Without it a renewal failure survives only until the next probe: the health
// reconciler rewrites agent_last_error every sweep, so a successful ping would
// clear the warning within a minute and the fleet would read healthy right up
// to the day its certificates expire.
func WithRenewalWatch(w RenewalWatch) QubeServiceOption {
	return func(s *QubeServiceImpl) { s.renewals = w }
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

// Create records the qube and provisions it.
//
// Until now this only wrote a database row: the UI reported a qube that had no
// VM behind it. The row is still written first — it is what the tfvars renderer
// reads, so terraform cannot learn about the qube until it exists — and the
// provision job is then queued against it.
func (s *QubeServiceImpl) Create(ctx context.Context, req *models.QubeCreateRequest) (*Operation, error) {
	if err := s.validateQubeCreateRequest(ctx, req); err != nil {
		return nil, err
	}

	qube := buildNewQube(req)
	applyDefaultSpec(qube)
	// Resolve encryption: a request that left encrypt_data unset (nil) inherits
	// the fleet default; an explicit true/false is honored. After this the stored
	// value is always concrete, so readers never see nil for a created qube.
	if qube.Spec.EncryptData == nil {
		d := s.encryptDataDefault
		qube.Spec.EncryptData = &d
	}
	// Start in pending so the claim below has a defined source status.
	qube.Status = models.QubeStatusPending

	// Resolve placement BEFORE writing the row, and persist the concrete node.
	// Recomputing it on every apply would let a qube drift between nodes as
	// cluster load changes, which terraform would see as a reason to rebuild the
	// VM. Deciding once and recording the answer also makes "why is it here?"
	// answerable later.
	if req.ZoneID != "" {
		zone, err := s.zoneRepo.GetByID(ctx, req.ZoneID)
		if err != nil {
			return nil, ErrZoneNotFound
		}
		node, reason, err := s.resolvePlacement(ctx, qube, zone)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPlacement, err)
		}
		qube.Spec.Node = node
		if node != "" {
			log.Printf("scheduler: placing qube %q on node %q (%s)", qube.Name, node, reason)
		} else {
			log.Printf("scheduler: qube %q has no node yet (%s)", qube.Name, reason)
		}
	}

	if err := s.qubeRepo.Create(ctx, qube); err != nil {
		return nil, err
	}

	// Mint the agent's bootstrap credential now, while the qube row exists to
	// own it and before any infrastructure is built. Later would mean a running
	// remote with no way to authenticate; earlier would leave a token with no
	// qube to invalidate it against.
	//
	// This does NOT produce a certificate — the agent generates its own key at
	// first boot and BootstrapMonitor signs it. So the qube is deliberately
	// created uncertified, and stays that way until the console reaches it.
	if s.issuer != nil {
		if err := s.issuer.IssueFor(ctx, qube); err != nil {
			// The qube row is left in place deliberately: it already exists, and
			// deleting it here would race the caller's own view. A qube without a
			// credential is visible and retryable; a half-deleted one is not.
			return nil, fmt.Errorf("%w: mint agent bootstrap credential: %v", ErrOrchestration, err)
		}
	}

	return s.claimAndEnqueue(ctx, qube,
		[]models.QubeStatus{models.QubeStatusPending},
		models.QubeStatusCreating, orchestrator.ActionProvision, models.QubeStatusError, nil)
}

// validateQubeCreateRequest validates qube creation request.
func (s *QubeServiceImpl) validateQubeCreateRequest(ctx context.Context, req *models.QubeCreateRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("qube name is required")
	}

	if !req.Type.IsValid() {
		return ErrInvalidQubeType
	}

	// The name becomes a terraform map key and a -target address, so it must be
	// safe there. Rejecting it here turns what used to be a confusing failure at
	// first Start (or, before Create provisioned anything, a qube that could
	// never be started at all) into an immediate, actionable 400.
	if !orchestrator.ValidQubeName(req.Name) {
		return fmt.Errorf("%w: %q", ErrInvalidQubeName, req.Name)
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
// Delete releases a qube: terraform destroys the compute instance while the
// data disk, and the storage-holder VM that owns it, are retained.
//
// This is deliberately not a teardown, and the database row is deliberately
// kept. The storage holder carries lifecycle.prevent_destroy, so destroying it
// is a plan-time error rather than something a DELETE can perform; and dropping
// the qube from the rendered terraform variables while its storage VM is still
// in state does not bypass that guard — it wedges every subsequent apply, for
// every qube. Discarding the data is therefore a separate, explicitly confirmed
// action, and until then the qube must keep being rendered.
//
// Unlike before, this no longer requires the qube to be stopped: releasing a
// running qube is exactly the operation that stops it.
func (s *QubeServiceImpl) Delete(ctx context.Context, id string) error {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return ErrQubeNotFound
	}
	if qube.Status == models.QubeStatusReleased {
		return nil // already released; releasing again is a no-op
	}

	// NOTE: certificates are deliberately NOT revoked here. Release keeps the
	// data disk and the qube can be resumed, which would need the same identity
	// again. Revocation belongs with a purge, when the qube genuinely goes away.
	_, err = s.claimAndEnqueue(ctx, qube,
		[]models.QubeStatus{
			models.QubeStatusRunning, models.QubeStatusStopped,
			models.QubeStatusSuspended, models.QubeStatusPending,
			models.QubeStatusError,
		},
		models.QubeStatusDeleting, orchestrator.ActionRelease, qube.Status, nil)
	return err
}

// Start starts (resumes) a qube.
//
// Order matters: preconditions are checked first, then the orchestrator rebuilds
// the compute instance (terraform resume), and only if that SUCCEEDS is the DB
// status flipped to running. If orchestration fails the DB status is left
// untouched and an error is returned — we never report "running" for a qube the
// infrastructure did not actually bring up.
// claimAndEnqueue moves the qube into a transient status and queues the work.
//
// The claim comes first and is atomic: it both validates that the operation
// makes sense from the current status and reserves the qube, so a double click
// cannot enqueue two multi-minute applies. If queueing then fails we roll the
// status back, otherwise the qube would be stuck "busy" with nothing running.
func (s *QubeServiceImpl) claimAndEnqueue(
	ctx context.Context,
	qube *models.Qube,
	from []models.QubeStatus,
	to models.QubeStatus,
	action orchestrator.Action,
	revertTo models.QubeStatus,
	prepare func(context.Context) error,
) (*Operation, error) {
	if err := s.qubeRepo.ClaimTransition(ctx, qube.ID, from, to); err != nil {
		return nil, err
	}

	// Work that must happen after the qube is CLAIMED but before anything is
	// enqueued. The claim is the only serialization point on this path, and
	// certificate reissue depends on it: two concurrent resumes that each minted
	// an identity before claiming would each revoke the other's, and whichever
	// one terraform delivered, the qube would boot holding a revoked certificate
	// and be locked out — by the code written to prevent lockout.
	//
	// A failure here releases the claim, for the same reason a failed enqueue
	// does: nothing is running, so leaving the qube pinned in a transient status
	// would make it permanently "busy".
	if prepare != nil {
		if err := prepare(ctx); err != nil {
			_ = s.qubeRepo.UpdateStatus(ctx, qube.ID, revertTo)
			return nil, err
		}
	}

	// No submitter configured: run inline and settle the status here. This keeps
	// the console usable (and tests synchronous) without an orchestration queue.
	if s.submitter == nil {
		if err := s.runInline(ctx, qube, action); err != nil {
			_ = s.qubeRepo.UpdateStatus(ctx, qube.ID, models.QubeStatusError)
			return nil, fmt.Errorf("%w: %s %q: %v", ErrOrchestration, action, qube.Name, err)
		}
		if err := s.qubeRepo.UpdateStatus(ctx, qube.ID, terminalStatusFor(action)); err != nil {
			return nil, err
		}
		// Mirror the async completion hook: a destroyed compute VM's IP is stale,
		// and a resume draws a fresh DHCP lease, so clear it and let the address
		// reader re-learn the real one. Without this a resumed qube is dialed at a
		// dead address forever. See makeCompletionHook in cmd/server.
		if ComputeDestroyingAction(action) {
			if err := s.qubeRepo.UpdateIPAddress(ctx, qube.ID, ""); err != nil {
				return nil, err
			}
		}
		updated, err := s.qubeRepo.GetByID(ctx, qube.ID)
		if err != nil {
			return nil, err
		}
		return &Operation{Qube: updated}, nil
	}

	job, err := s.submitter.Submit(ctx, qube.ID, qube.Name, action)
	if err != nil {
		// Nothing is running, so release the claim rather than leaving the qube
		// pinned in a transient status forever.
		_ = s.qubeRepo.UpdateStatus(ctx, qube.ID, revertTo)
		return nil, fmt.Errorf("%w: enqueue %s %q: %v", ErrOrchestration, action, qube.Name, err)
	}

	updated, err := s.qubeRepo.GetByID(ctx, qube.ID)
	if err != nil {
		return nil, err
	}
	return &Operation{Qube: updated, JobID: job.ID}, nil
}

// runInline performs the action synchronously (no queue configured).
func (s *QubeServiceImpl) runInline(ctx context.Context, qube *models.Qube, action orchestrator.Action) error {
	switch action {
	case orchestrator.ActionResume:
		return s.executor.Resume(ctx, qube.Name)
	case orchestrator.ActionSuspend, orchestrator.ActionRelease:
		return s.executor.Suspend(ctx, qube.Name)
	case orchestrator.ActionProvision:
		return s.executor.Provision(ctx, qube.Name)
	case orchestrator.ActionDestroy:
		return s.executor.Destroy(ctx, qube.Name)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

// terminalStatusFor maps an action to the status a successful run lands on.
//
// It still maps a successful provision to "running" unconditionally, and that
// is correct: this says the COMPUTE VM is up, which is exactly what a completed
// terraform apply establishes. Whether the agent inside it works is a separate
// fact, tracked in Qube.AgentHealth. Folding a dead agent into this status would
// make "suspended" and "running but unusable" indistinguishable and would lose
// the only signal that tells them apart.
//
// The agent probe is deliberately NOT triggered from here. Its only caller left
// is the inline path in claimAndEnqueue, which runs when no job submitter is
// configured — tests and consoles with no orchestration, where the executor is
// a no-op and there is no VM to probe. The real asynchronous path settles its
// status in the orchestrator's completion hook, so that is where the probe is
// hooked in (see makeCompletionHook in cmd/server).
func terminalStatusFor(action orchestrator.Action) models.QubeStatus {
	switch action {
	case orchestrator.ActionResume, orchestrator.ActionProvision:
		return models.QubeStatusRunning
	case orchestrator.ActionSuspend:
		return models.QubeStatusSuspended
	case orchestrator.ActionRelease:
		return models.QubeStatusReleased
	case orchestrator.ActionDestroy:
		return models.QubeStatusReleased
	default:
		return models.QubeStatusError
	}
}

// ComputeDestroyingAction reports whether an action tears down the compute VM,
// which is exactly when the recorded IP stops being valid. Suspend and release
// both set compute_running=false (the VM is destroyed, its disk retained), and
// destroy removes the qube outright; a resume then rebuilds the VM with a new
// MAC and a new DHCP lease. The stored address must be cleared on all three so
// it is re-read from terraform rather than believed forever. Exported so the
// async completion hook (cmd/server) and this inline path share one definition.
func ComputeDestroyingAction(action orchestrator.Action) bool {
	switch action {
	case orchestrator.ActionSuspend, orchestrator.ActionRelease, orchestrator.ActionDestroy:
		return true
	default:
		return false
	}
}

// Start resumes a qube: terraform rebuilds the compute VM and re-attaches the
// existing data disk. It does not wait — that takes minutes on a real cluster.
// The qube goes to "resuming" immediately and reaches "running" or "error" when
// the job finishes; poll the returned job id.
func (s *QubeServiceImpl) Start(ctx context.Context, id string) (*Operation, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}
	if err := s.verifyZoneConnected(ctx, qube.ZoneID); err != nil {
		return nil, err
	}

	// Captured before the claim overwrites it: the reissue decision below turns
	// on whether a compute instance exists RIGHT NOW, and by the time the hook
	// runs the status already says "resuming".
	prior := qube.Status

	return s.claimAndEnqueue(ctx, qube,
		[]models.QubeStatus{
			models.QubeStatusStopped, models.QubeStatusSuspended,
			models.QubeStatusReleased, models.QubeStatusError,
		},
		models.QubeStatusResuming, orchestrator.ActionResume, qube.Status,
		func(ctx context.Context) error { return s.reissueIdentityForResume(ctx, qube, prior) })
}

// reissueIdentityForResume hands a resuming qube a brand new agent identity.
//
// This closes the suspended-qube lockout. Renewal runs over the agent's own
// mTLS channel, and suspend DESTROYS the compute instance, so a suspended qube
// has nothing to renew against — the renewal sweep skips it, correctly. Left
// alone, a qube parked across its whole renewal window is resumed with an
// expired certificate, the agent refuses to start without a valid one, and the
// qube is locked out for good with the repair channel being the very thing that
// is broken. Reissuing here is not renewing harder; it is delivering the
// identity through the channel that is actually open at that moment.
//
// It is free rather than merely acceptable, and that was checked rather than
// assumed. Resume drives compute_running false -> true, which makes
// proxmox_virtual_environment_vm.compute go from count=0 to count=1 — the
// instance is CREATED, not modified. The identity snippet
// (proxmox_virtual_environment_file.agent_identity) is gated on the same
// variable and is an upstream dependency of the compute VM, so the targeted
// resume apply creates it too, reading whatever content is at the path at that
// moment. The filesha256 checksum that normally forces a replacement has
// nothing to replace: neither resource exists yet. See
// terraform/modules/remote-qube-base/providers/proxmox/main.tf and the
// agent_user_data_file wiring in tfvars.go.
//
// The computeRunning guard is what keeps that true. Every status Start accepts
// today renders compute_running=false, so it never fires — but it is the single
// predicate that decides whether terraform builds a compute VM, so if that
// accepted-source list ever grows to include a status with a LIVE instance, the
// reissue is skipped instead of quietly changing the identity file underneath a
// running VM and provoking a rebuild nobody asked for.
func (s *QubeServiceImpl) reissueIdentityForResume(
	ctx context.Context, qube *models.Qube, prior models.QubeStatus,
) error {
	if s.issuer == nil {
		return nil
	}
	// Only statuses that PROVE the compute instance was destroyed get a reissue.
	//
	// The guard used to be computeRunning(prior), which answers a different
	// question: "should terraform build a VM", not "is one running". Error is
	// exactly where those diverge, and it is reached with a live VM routinely —
	// reconcileStrandedQubes rewrites every Creating/Resuming qube to Error when
	// the console restarts, including ones whose apply had already finished and
	// whose agent is healthy.
	//
	// Starting such a qube would revoke the certificate of a RUNNING agent and
	// then not replace anything: terraform sees the compute VM already matching
	// compute_running=true, so it is not rebuilt and never re-reads cloud-init.
	// The result is a healthy VM whose agent is refused by both the prober and
	// the renewer — unreachable, unrenewable, recoverable only by rebuilding.
	//
	// Same asymmetry as discardUninstalled: withdrawing a credential that is
	// still in use is unrecoverable, while keeping one that is already gone
	// merely leaves a stale row. When the state does not tell us which we are
	// looking at, we take the recoverable one.
	if !instanceProvablyDestroyed(prior) {
		log.Printf("pki: qube %q is %s, which does not prove its compute instance is gone; "+
			"keeping its current identity rather than risk revoking a certificate a live agent is using",
			qube.Name, prior)
		return nil
	}

	// A failure here aborts the resume. That is the safe direction: without a
	// delivered identity the qube would come up and its agent would refuse to
	// start anyway, so reporting "resuming" would promise something that cannot
	// happen — and this project's recurring defect is exactly the failure that
	// looks like success.
	if err := s.issuer.ReissueFor(ctx, qube,
		fmt.Sprintf("compute instance rebuilt on resume from %s", prior)); err != nil {
		return fmt.Errorf("%w: reissue agent identity for %q: %v", ErrOrchestration, qube.Name, err)
	}
	return nil
}

// instanceProvablyDestroyed reports whether a status GUARANTEES there is no
// compute instance, and therefore no agent that could still be holding the
// current certificate.
//
// Deliberately narrow. This gates revocation, so "probably gone" is not good
// enough — only the statuses terraform reaches by actually destroying the
// instance qualify. Anything else, including Error and Stopped, may or may not
// have a live VM behind it, and the cost of guessing wrong is a qube that can
// never authenticate again.
func instanceProvablyDestroyed(status models.QubeStatus) bool {
	switch status {
	case models.QubeStatusSuspended, models.QubeStatusReleased:
		return true
	default:
		return false
	}
}

// Stop suspends a qube: terraform destroys the compute VM and keeps the data
// disk. Asynchronous, same contract as Start.
func (s *QubeServiceImpl) Stop(ctx context.Context, id string) (*Operation, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrQubeNotFound
	}
	return s.claimAndEnqueue(ctx, qube,
		[]models.QubeStatus{models.QubeStatusRunning, models.QubeStatusError},
		models.QubeStatusSuspending, orchestrator.ActionSuspend, qube.Status, nil)
}

// verifyZoneConnected checks if the zone is connected.
func (s *QubeServiceImpl) verifyZoneConnected(ctx context.Context, zoneID string) error {
	zone, err := s.zoneRepo.GetByID(ctx, zoneID)
	if err != nil {
		return ErrZoneNotFound
	}

	if zone.Status != models.ZoneStatusConnected {
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

// CheckReachable answers "is this qube's agent alive?" on demand.
//
// It is now a thin wrapper over ProbeAgent rather than a second implementation.
// It used to call the global transport directly, which meant the endpoint and
// any background check could give DIFFERENT answers for the same qube — the
// on-demand path asking a fixed configured endpoint while the recorded health
// came from the qube's own address. Two sources of truth for one fact is how
// the duplicate systemd unit went unnoticed earlier in this project.
//
// A consequence worth stating: a manual check now updates the stored health,
// because a probe is a probe whoever asked for it.
func (s *QubeServiceImpl) CheckReachable(ctx context.Context, id string) (string, error) {
	qube, err := s.qubeRepo.GetByID(ctx, id)
	if err != nil {
		return "", ErrQubeNotFound
	}
	if err := s.verifyZoneConnected(ctx, qube.ZoneID); err != nil {
		return "", err
	}

	res := s.ProbeAgent(ctx, qube, AgentProbeSteady)
	if !res.Reachable {
		// The real reason is carried through, not flattened: "unreachable"
		// alone is what sends an operator to SSH into a hypervisor node.
		return "", fmt.Errorf("%w: ping %q: %s", ErrUnreachable, qube.Name, res.Reason)
	}
	return res.Pong, nil
}

// ProbeAgent probes one qube's agent and records the outcome on the qube row.
//
// It returns no error, deliberately: "the agent did not answer" is a successful
// probe with a bad result, and every caller — an HTTP handler, the settle loop,
// the reconciler — needs to record it rather than decide whether to abort. In
// particular a failed probe must never fail provisioning.
func (s *QubeServiceImpl) ProbeAgent(
	ctx context.Context, qube *models.Qube, phase AgentProbePhase,
) AgentProbeResult {
	if qube == nil {
		return AgentProbeResult{Status: AgentProbeNotConfigured, Reason: "no qube given"}
	}

	// A qube with no compute instance is not an unreachable qube. Suspend
	// destroys the instance and keeps the data disk, so "nothing answered" is
	// the expected and correct state of a parked qube — the same reasoning that
	// made the settle phase necessary for booting ones. Recording it as
	// unreachable would paint every suspended qube red for as long as it stayed
	// parked, and a field that is red for expected states stops being read.
	//
	// The decision lives HERE rather than in each caller so the reconciler, the
	// settle loop and the on-demand endpoint cannot disagree about it — the same
	// single-answer discipline ProbeAgent already enforces for everything else.
	if !computeRunning(qube.Status) {
		res := AgentProbeResult{
			QubeID: qube.ID, QubeName: qube.Name, CheckedAt: time.Now().UTC(),
			Status: AgentProbeNoCompute,
			Reason: fmt.Sprintf(
				"qube %q is %s: suspend destroys the compute instance, so there is no agent to probe "+
					"(it gets a fresh identity when it is resumed)", qube.Name, qube.Status),
		}
		s.recordAgentHealth(ctx, qube, res, phase)
		return res
	}

	res := s.runAgentProbe(ctx, qube)
	s.recordAgentHealth(ctx, qube, res, phase)
	return res
}

// runAgentProbe performs the probe itself, preferring the per-qube prober.
func (s *QubeServiceImpl) runAgentProbe(ctx context.Context, qube *models.Qube) AgentProbeResult {
	if s.prober != nil {
		return s.prober.Probe(ctx, qube)
	}

	// Fallback: the single global transport. It is pinned to one configured
	// RemoteEndpoint, so it answers for THAT remote regardless of which qube was
	// asked about — useful only in the single-remote deployment it was built
	// for. Said out loud on every use: a wrong answer that looks like a right
	// one is the failure mode this whole feature exists to remove.
	log.Printf("agentprobe: no per-qube prober configured, falling back to the global transport for qube %q; "+
		"the result describes the configured remote endpoint, not necessarily this qube", qube.Name)

	started := time.Now()
	res := AgentProbeResult{
		QubeID: qube.ID, QubeName: qube.Name, CheckedAt: started.UTC(),
	}
	resp, err := s.transport.Call(ctx, qube.Name, pingService, nil)
	res.Duration = time.Since(started)
	res.LatencyMS = res.Duration.Milliseconds()
	if err != nil {
		// No transport at all means nothing was attempted, which is "unknown",
		// not "unhealthy" — the distinction the health field is built on.
		res.Status = AgentProbeRPCFailed
		if errors.Is(err, transport.ErrNoTransport) {
			res.Status = AgentProbeNotConfigured
		}
		res.Reason = fmt.Sprintf("ping %q over the global transport failed: %v", qube.Name, err)
		return res
	}
	res.Reachable = true
	res.Status = AgentProbeOK
	res.Pong = strings.TrimSpace(string(resp))
	return res
}

// recordAgentHealth persists one probe outcome, and never anything else.
//
// It writes through UpdateAgentHealth, which touches only the agent_* columns:
// probes run concurrently with provisioning, and a read-modify-write of the
// whole row here would revert a status the orchestrator had just set.
func (s *QubeServiceImpl) recordAgentHealth(
	ctx context.Context, qube *models.Qube, res AgentProbeResult, phase AgentProbePhase,
) {
	health := agentHealthForResult(res, phase)

	// Unknown is RECORDED, not skipped.
	//
	// Returning early here looked conservative and was the opposite: the row
	// kept its previous "healthy" and its old probe timestamp, so a console that
	// had LOST the ability to probe (unusable CA, failed decrypt after a key
	// rotation) went on presenting a stale green verdict indistinguishable from
	// one confirmed a second ago. The agent could be dead the whole time.
	//
	// Writing unknown with a fresh timestamp and the reason makes the loss of
	// visibility itself visible — which is the entire point of this field.

	// A probe that somehow reported no time still happened. Storing the zero
	// time would render as year 1 in the UI, which reads as corruption rather
	// than as the recent observation it is.
	probedAt := res.CheckedAt
	if probedAt.IsZero() {
		probedAt = time.Now().UTC()
	}

	// A qube whose agent answers but whose certificate is not being renewed is
	// healthy TODAY and dead on a known date. That warning is re-attached on
	// every write rather than recorded once by the renewal monitor, because this
	// function overwrites agent_last_error every sweep — a warning written once
	// would be erased by the next successful probe, and the fleet would look
	// perfectly healthy until the certificates ran out.
	failure := res.Reason
	// ...but only while there is a compute instance for the warning to be about.
	// A suspended qube has no agent to renew against, so whatever the monitor
	// last remembered describes an instance that no longer exists — and it will
	// be handed a fresh certificate the moment it is resumed. Republishing
	// "certificate EXPIRED, this qube can no longer authenticate" against a
	// parked qube is a permanent red mark for an expected state, which is how a
	// health field stops being believed.
	if computeRunning(qube.Status) {
		if warn := s.renewalWarning(qube.ID); warn != "" {
			if failure == "" {
				failure = warn
			} else {
				failure = failure + "; " + warn
			}
		}
	}

	// Detached from the caller's deadline: the observation already exists, and
	// dropping it because an HTTP request was canceled a millisecond later
	// would leave the console reporting a health reading it has disproved.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := s.qubeRepo.UpdateAgentHealth(ctx, qube.ID, health, probedAt, failure); err != nil {
		if errors.Is(err, repository.ErrQubeNotFound) {
			// Deleted between probe and write. Expected, and not worth a scary
			// line — but not silent either, because a probe loop that writes
			// nothing at all looks exactly the same from outside.
			log.Printf("agentprobe: qube %q disappeared before its probe result could be recorded", qube.Name)
			return
		}
		log.Printf("agentprobe: qube %q probed %s but recording it failed: %v", qube.Name, health, err)
	}
}

// renewalWarning is the outstanding certificate-renewal problem for a qube.
func (s *QubeServiceImpl) renewalWarning(qubeID string) string {
	if s.renewals == nil {
		return ""
	}
	return s.renewals.RenewalWarning(qubeID)
}

// agentHealthFor turns a probe status into the health to store.
//
// The mapping lives in exactly one place on purpose: an endpoint and a
// background loop that classified the same probe differently would put two
// contradictory readings into the same column.
// agentHealthForResult maps a whole probe result, honoring authority.
//
// A non-authoritative success must NOT become healthy. The global transport is
// pinned to one endpoint whose invoker ignores the target name (see
// internal/agent/invoker.go: "target carries no authority"), so it answers for
// every qube alike. Recording that as healthy stores a verdict about a machine
// nothing ever contacted — a qube with no address at all would be marked green
// off a pong that named some other remote.
func agentHealthForResult(res AgentProbeResult, phase AgentProbePhase) models.AgentHealth {
	h := agentHealthFor(res.Status, phase)
	if h == models.AgentHealthHealthy && !res.Authoritative {
		return models.AgentHealthUnknown
	}
	return h
}

func agentHealthFor(status AgentProbeStatus, phase AgentProbePhase) models.AgentHealth {
	switch {
	case status == AgentProbeOK:
		return models.AgentHealthHealthy
	case status == AgentProbeNotConfigured:
		// This console cannot probe. It has learned nothing about the agent and
		// must not pretend otherwise — see AgentHealthUnknown.
		return models.AgentHealthUnknown
	case status == AgentProbeNoCompute:
		// There is no agent to have an opinion about: the instance was destroyed
		// by suspend. Unknown regardless of phase — "starting" would claim it is
		// on its way up and "unreachable" would claim it is broken, and a parked
		// qube is neither.
		return models.AgentHealthUnknown
	case phase == AgentProbeSettling:
		// Inside the post-boot budget: the agent is not up yet, which for a
		// brand new qube is the expected state rather than a fault.
		return models.AgentHealthStarting
	default:
		return models.AgentHealthUnreachable
	}
}
