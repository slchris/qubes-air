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
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	ZoneID    string     `json:"zone_id"`
	Type      QubeType   `json:"type"`
	Status    QubeStatus `json:"status"`
	IPAddress string     `json:"ip_address,omitempty"`
	Spec      QubeSpec   `json:"spec"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`

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
	// the terraform compute/storage separation (compute_running).
	QubeStatusSuspended QubeStatus = "suspended"

	// Transient statuses: a terraform job for this qube is queued or running.
	//
	// These are claims as much as descriptions. A transition into one is made
	// atomically and only from an expected source status, which is what stops a
	// double-clicked button from enqueuing two applies against the same qube.
	// Terraform operations here take minutes, so this window is wide.
	QubeStatusResuming   QubeStatus = "resuming"
	QubeStatusSuspending QubeStatus = "suspending"
	QubeStatusDeleting   QubeStatus = "deleting"

	// QubeStatusReleased means the compute VM is gone and the qube has been
	// removed from the user's active list, but its data disk (and the
	// storage-holder VM that owns it) still exist.
	//
	// This is the resting state after a "delete" in the UI. Purging the disk is
	// a separate, explicitly confirmed action, because the storage holder
	// carries lifecycle.prevent_destroy and destroying it is irreversible.
	// Critically, a released qube must STILL be rendered into the terraform
	// variables: dropping it from the map while its storage VM remains in state
	// does not bypass prevent_destroy — it wedges every subsequent apply, for
	// every qube.
	QubeStatusReleased QubeStatus = "released"

	QubeStatusError QubeStatus = "error"
)

// IsValid checks if the qube status is valid.
func (s QubeStatus) IsValid() bool {
	switch s {
	case QubeStatusPending, QubeStatusCreating, QubeStatusRunning,
		QubeStatusStopped, QubeStatusSuspended, QubeStatusError,
		QubeStatusResuming, QubeStatusSuspending, QubeStatusDeleting,
		QubeStatusReleased:
		return true
	default:
		return false
	}
}

// IsTransient reports whether a terraform job is expected to be in flight for
// this qube.
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

	// NOTE: a Template string field used to live here. It was never consumed —
	// only the zone's TemplateVMID selects an image — so it implied an OS choice
	// the code did not make. Removed rather than left to mislead.
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
