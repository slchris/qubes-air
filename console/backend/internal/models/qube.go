// Package models defines the core domain types for Qubes Air.
package models

import "time"

// Qube represents a remote virtual machine instance.
//
// Status and AgentHealth are DIFFERENT FACTS and are deliberately not merged.
// Status says what we asked the hypervisor for and what it reports about the
// compute instance; AgentHealth says whether the agent inside that instance
// answered a probe. A VM can be genuinely running while its agent is dead —
// the package failed to install, the unit will not start, the hash did not
// match — and that qube is running, with an unhealthy agent. Folding the two
// together would make "suspended" and "the agent is not answering"
// indistinguishable, and would throw away the only signal that separates a
// working qube from an unusable one.
//
// This distinction is not theoretical. A stale cloud-init snippet once meant
// the agent was never installed at all: the job reported succeeded, the status
// read running, and every console-side signal stayed green for hours. The bug
// was found only by SSHing to a hypervisor node and running systemctl by hand.
// These fields exist so that never happens silently again.
type Qube struct {
	// PurgeRequested is irreversible intent; it survives failures and forbids resume.
	PurgeRequested bool       `json:"purge_requested"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	ZoneID         string     `json:"zone_id"`
	Type           QubeType   `json:"type"`
	Status         QubeStatus `json:"status"`
	IPAddress      string     `json:"ip_address,omitempty"`
	Spec           QubeSpec   `json:"spec"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// AgentHealth is the result of the most recent agent probe. Never omitted
	// from the API: an absent field would read as "this console has no opinion",
	// when the honest answer for a qube nobody has probed is "unknown".
	AgentHealth AgentHealth `json:"agent_health"`
	// AgentLastProbedAt is when a probe last ran, whatever its outcome. Nil
	// means never probed — distinct from a probe that ran and failed, which is
	// why this is a pointer and not a zero time.
	AgentLastProbedAt *time.Time `json:"agent_last_probed_at,omitempty"`
	// AgentLastHealthyAt is when the agent last actually answered. It is the
	// field that answers "how long has this been broken", which a bare
	// unreachable status cannot.
	AgentLastHealthyAt *time.Time `json:"agent_last_healthy_at,omitempty"`
	// AgentLastError is why the most recent probe failed, empty when it
	// succeeded. It always describes the LAST probe, not the last failure ever
	// seen, so a recovered agent does not keep displaying a stale complaint.
	// Carrying the real error is the point: "unreachable" alone sends an
	// operator to SSH, "x509: certificate signed by unknown authority" does not.
	AgentLastError string `json:"agent_last_error,omitempty"`

	// AgentFailingSince is when the CURRENT uninterrupted run of failed probes
	// began — the first probe the console recorded as unreachable after an
	// answer or after it had no verdict at all. It is cleared as soon as
	// anything else is recorded, so it never describes a previous instance of
	// the qube.
	//
	// It exists because "unreachable" alone cannot say whether waiting is
	// reasonable. Without a start time there is no way to answer "for how long
	// has nobody been listening" from the stored row, and the answer is what
	// separates a qube that is still coming up from one whose agent gave up.
	// See ClassifyAgentRecovery and AgentStartLimitBudget.
	AgentFailingSince *time.Time `json:"agent_failing_since,omitempty"`

	// AgentRecovery is DERIVED from the fields above when a row is read, never
	// stored: see ClassifyAgentRecovery. It answers the one question
	// AgentHealth cannot — whether an agent that is not answering may still
	// come back on its own.
	AgentRecovery AgentRecovery `json:"agent_recovery,omitempty"`
}

// AgentRecovery classifies an ongoing agent failure by whether it can still
// resolve without a human.
//
// It is the console's OBSERVATION of its own probes, not a reading of the
// guest's systemd. The console has no channel into the guest beyond the agent's
// mTLS listener, so it cannot see a unit's state, and a filtered port, a
// package that was never installed and a unit that hit its start limit all look
// identical from here. What it CAN see is how long nothing has answered, and
// that is enough to stop telling an operator "wait and see" forever: the shipped
// unit (packaging/agent-deb/qubes-air-agent.service) can only restart itself a
// bounded number of times in a bounded window, so once a failure has outlasted
// that budget no automatic recovery can still be in flight.
type AgentRecovery string

// Agent recovery constants.
const (
	// AgentRecoveryNone means there is no ongoing failure to recover from: the
	// agent answered, or this console has no verdict about it.
	AgentRecoveryNone AgentRecovery = "none"
	// AgentRecoveryPending means probes are failing, but not yet for longer
	// than the agent unit's own start-limit budget, so an automatic restart may
	// still be in flight. This is the ordinary state of an agent that is
	// restarting or of a qube whose port is briefly unreachable.
	AgentRecoveryPending AgentRecovery = "pending"
	// AgentRecoveryManual means nothing has answered for longer than every
	// automatic start the agent unit's policy permits (see
	// AgentStartLimitBudget), so waiting cannot fix it: a human has to act.
	//
	// It does NOT mean "the unit hit its start limit". It means the failure has
	// outlasted that budget — which is also true of causes that have nothing to
	// do with systemd, such as a filtered port, a firewall, a package that was
	// never installed or a qube that cannot reach the console. What it rules
	// out is only the case it was built for: a bounded retry still running.
	// docs/runbook-remotevm.md §11 has the guest-side check that tells the
	// possibilities apart.
	AgentRecoveryManual AgentRecovery = "manual"
)

// AgentStartLimitBudget is how long an agent failure must persist before the
// console stops describing it as something that may still resolve on its own.
//
// DERIVED from the shipped unit, not chosen:
// packaging/agent-deb/qubes-air-agent.service:10-11 sets
// StartLimitIntervalSec=300 and StartLimitBurst=5, and :32-33 sets
// Restart=on-failure with RestartSec=5. Every automatic start that policy
// permits therefore happens within (5-1)*5s = 20s of the first failure, and the
// rate limiter's window closes 300s after it opened. Once a failure has
// outlasted that window, no systemd-side retry can still be in flight.
//
// It is deliberately not configurable: it describes the packaged unit, not a
// console preference. TestAgentStartLimitBudgetMatchesTheShippedUnit reads that
// unit and fails if the two drift apart.
const AgentStartLimitBudget = 300 * time.Second

// ClassifyAgentRecovery derives the recovery reading for a stored row.
//
// Pure in the row's own recorded times, deliberately: the classification may
// not consult the wall clock. A console that stopped probing (interval set to
// 0, or a dead prober) would otherwise let an old failure keep aging itself
// into "manual recovery" with nobody watching, which is the same false signal
// this field exists to prevent. Only the gap between two observations the
// console actually made counts.
func ClassifyAgentRecovery(q *Qube) AgentRecovery {
	if q == nil || q.AgentHealth != AgentHealthUnreachable {
		// Healthy, starting or unknown: there is no failure streak to classify,
		// and "unknown" must never be rendered as an alarm.
		return AgentRecoveryNone
	}
	if q.AgentFailingSince == nil || q.AgentLastProbedAt == nil {
		// A row from an older schema, or one a caller built by hand: failing,
		// but with no recorded span. "Pending" is the honest reading — the
		// console has not observed long enough to claim more — and the next
		// probe fills the streak in.
		return AgentRecoveryPending
	}
	if q.AgentLastProbedAt.Sub(*q.AgentFailingSince) >= AgentStartLimitBudget {
		return AgentRecoveryManual
	}
	return AgentRecoveryPending
}

// AgentHealth is what the console knows about the agent inside a qube, as
// opposed to QubeStatus, which is what it knows about the VM itself.
type AgentHealth string

// Agent health constants.
//
// Deliberately a small set. Every value here is something the console has
// actually observed or honestly admits it has not; there is no "degraded" or
// "warning" state, because nothing in the probe path can distinguish one.
const (
	// AgentHealthUnknown means no probe has completed for this qube yet — a
	// brand new qube, or one whose probes have never run. It is NOT a synonym
	// for healthy, and must never be rendered as one.
	AgentHealthUnknown AgentHealth = "unknown"
	// AgentHealthHealthy means the agent answered a probe.
	AgentHealthHealthy AgentHealth = "healthy"
	// AgentHealthUnreachable means a probe ran and did not get an answer. The
	// reason is in AgentLastError.
	AgentHealthUnreachable AgentHealth = "unreachable"
	// AgentHealthStarting means probes are running inside a freshly booted
	// qube's grace period and have not answered YET.
	//
	// This is an observation, not a warning level: cloud-init downloads and
	// installs the agent only after the VM reports its address, so the agent is
	// reliably absent for the first minute or two of a qube's life. Without this
	// value every healthy qube would read "unreachable" immediately after
	// provisioning — which is worse than reporting nothing, because it teaches
	// operators that the field is noise and then they ignore the one time it is
	// real. It becomes healthy or unreachable when the grace period resolves;
	// it must never be the resting state.
	AgentHealthStarting AgentHealth = "starting"
)

// IsValid checks if the agent health value is valid.
func (h AgentHealth) IsValid() bool {
	switch h {
	case AgentHealthUnknown, AgentHealthHealthy, AgentHealthUnreachable, AgentHealthStarting:
		return true
	default:
		return false
	}
}

// QubeType defines the type of qube workload.
type QubeType string

// Qube type constants.
const (
	QubeTypeApp  QubeType = "app"  // Application
	QubeTypeWork QubeType = "work" // Workstation
	QubeTypeDev  QubeType = "dev"  // Development environment
	QubeTypeGPU  QubeType = "gpu"  // GPU compute
	QubeTypeDisp QubeType = "disp" // Disposable VM
	QubeTypeSys  QubeType = "sys"  // System service
)

// IsValid checks if the qube type is valid.
func (t QubeType) IsValid() bool {
	switch t {
	case QubeTypeApp, QubeTypeWork, QubeTypeDev, QubeTypeGPU, QubeTypeDisp, QubeTypeSys:
		return true
	default:
		return false
	}
}

// QubeStatus represents the current state of a qube.
type QubeStatus string

// Qube status constants.
const (
	QubeStatusPending  QubeStatus = "pending"
	QubeStatusCreating QubeStatus = "creating"
	QubeStatusRunning  QubeStatus = "running"
	QubeStatusStopped  QubeStatus = "stopped"
	// QubeStatusSuspended means the compute instance has been released
	// (destroyed) to save cost while the persistent data disk is retained. It
	// is distinct from Stopped: a suspended qube can be resumed by rebuilding
	// compute and re-attaching the same disk. See the orchestrator package and
	// the provider compute/storage separation.
	QubeStatusSuspended QubeStatus = "suspended"

	// Transient statuses: an orchestration job for this qube is queued or
	// running.
	//
	// These are claims as much as descriptions. A transition into one is made
	// atomically and only from an expected source status, which is what stops a
	// double-clicked button from enqueuing two operations against the same qube.
	// Provider operations here take minutes, so this window is wide.
	QubeStatusResuming   QubeStatus = "resuming"
	QubeStatusSuspending QubeStatus = "suspending"
	QubeStatusDeleting   QubeStatus = "deleting"

	// QubeStatusReleased means the compute VM is gone and the qube has been
	// removed from the user's active list, but its data disk (and the
	// storage-holder VM that owns it) still exist.
	//
	// This is the resting state after a "delete" in the UI. Purging the disk is
	// a separate, explicitly confirmed action, because destroying the storage
	// holder is irreversible. Critically, a released qube must STILL be
	// resolvable in qube_infra: its protected data disk is what a purge has to
	// find, and dropping the record would orphan the disk with no way to destroy
	// it from the console.
	QubeStatusReleased QubeStatus = "released"

	// QubeStatusPurged means the qube was destroyed outright: the compute
	// instance AND its persistent data disk are gone, the agent identity has
	// been revoked and the RemoteVM registration removed. The row is kept as
	// history but is no longer resolvable or operable — there is no
	// infrastructure left to act on. Terminal.
	QubeStatusPurged QubeStatus = "purged"

	QubeStatusError QubeStatus = "error"
)

// IsValid checks if the qube status is valid.
func (s QubeStatus) IsValid() bool {
	switch s {
	case QubeStatusPending, QubeStatusCreating, QubeStatusRunning,
		QubeStatusStopped, QubeStatusSuspended, QubeStatusError,
		QubeStatusResuming, QubeStatusSuspending, QubeStatusDeleting,
		QubeStatusReleased, QubeStatusPurged:
		return true
	default:
		return false
	}
}

// IsTransient reports whether an orchestration job is expected to be in flight
// for this qube.
//
// The job queue lives in memory, so a qube found in a transient status at
// startup belongs to a job that died with the previous process. Startup
// reconciliation uses this to find them; without it they would stay stuck
// forever, and every future operation on them would be refused as "busy".
func (s QubeStatus) IsTransient() bool {
	switch s {
	case QubeStatusCreating, QubeStatusResuming, QubeStatusSuspending, QubeStatusDeleting:
		return true
	default:
		return false
	}
}

// QubeSpec defines resource specifications for a qube.
type QubeSpec struct {
	VCPU   int `json:"vcpu"`
	Memory int `json:"memory"` // Memory in MB
	// Disk is the OS/root disk in GB. It is recreated with the compute
	// instance, so it holds nothing that must survive a suspend.
	//
	// It must be LARGER than the template's disk: Proxmox cannot shrink a
	// disk, so a clone whose target size is below the template's fails.
	Disk int `json:"disk"`
	// DataDiskGB is the persistent data disk, owned by a separate
	// storage-holder VM and re-attached on every resume. This is what survives
	// suspend/resume, and what a purge would destroy.
	DataDiskGB int `json:"data_disk_gb,omitempty"`
	// Node pins this qube to a cluster node. Empty means the zone default.
	// Only meaningful with shared storage; with node-local storage the qube
	// must live where its template and disks are.
	Node string   `json:"node,omitempty"`
	GPU  *GPUSpec `json:"gpu,omitempty"`

	// EncryptData makes the data disk a LUKS container. The passphrase is
	// derived by the console and pushed to the agent over verified mTLS only
	// when the disk needs opening, so nothing on the untrusted remote — disk,
	// cloud-init, or backup — ever holds a key. The compute VM then boots
	// without /data until the console unlocks it. Off keeps the plaintext
	// auto-mount. Cannot be flipped on a qube that already has a plaintext data
	// disk; the agent refuses to overwrite existing data.
	//
	// A POINTER, not a bool, so a create request can leave it UNSET (nil) and
	// pick up the console's fleet default. nil and false both mean plaintext to
	// a reader (use EncryptsData); the distinction only matters at create, where
	// nil defers to the default and an explicit false opts out of an
	// encrypt-by-default fleet.
	EncryptData *bool `json:"encrypt_data,omitempty"`

	// NOTE: a Template string field used to live here. It was never consumed —
	// only the zone's TemplateVMID selects an image — so it implied an OS choice
	// the code did not make. Removed rather than left to mislead.
}

// EncryptsData reports whether this qube's data disk should be a LUKS
// container. It collapses the tri-state EncryptData pointer for readers: unset
// (nil) and explicit false both mean plaintext. Callers that need to
// distinguish "unset" from "false" — only the create path — look at the pointer
// directly.
func (s QubeSpec) EncryptsData() bool {
	return s.EncryptData != nil && *s.EncryptData
}

// GPUSpec defines GPU configuration.
type GPUSpec struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// QubeCreateRequest represents a request to create a new qube.
type QubeCreateRequest struct {
	Name   string   `json:"name" binding:"required"`
	ZoneID string   `json:"zone_id"` // Optional: qube can exist without a zone
	Type   QubeType `json:"type" binding:"required"`
	Spec   QubeSpec `json:"spec"`
}

// QubeUpdateRequest represents a request to update a qube.
type QubeUpdateRequest struct {
	Name *string   `json:"name,omitempty"`
	Spec *QubeSpec `json:"spec,omitempty"`
}
