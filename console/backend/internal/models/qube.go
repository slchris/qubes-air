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
	QubeStatusError    QubeStatus = "error"
)

// IsValid checks if the qube status is valid.
func (s QubeStatus) IsValid() bool {
	switch s {
	case QubeStatusPending, QubeStatusCreating, QubeStatusRunning, QubeStatusStopped, QubeStatusError:
		return true
	default:
		return false
	}
}

// QubeSpec defines resource specifications for a qube.
type QubeSpec struct {
	VCPU     int      `json:"vcpu"`
	Memory   int      `json:"memory"` // Memory in MB
	Disk     int      `json:"disk"`   // Disk in GB
	Template string   `json:"template,omitempty"`
	GPU      *GPUSpec `json:"gpu,omitempty"`
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
