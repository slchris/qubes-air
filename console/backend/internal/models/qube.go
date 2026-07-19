// Package models defines the core domain types for Qubes Air.
package models

import "time"

// Qube represents a remote virtual machine instance.
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
