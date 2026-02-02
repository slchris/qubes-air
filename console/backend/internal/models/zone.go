// Package models defines the core domain types for Qubes Air.
package models

import "time"

// Zone represents a remote infrastructure boundary.
type Zone struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Type      ZoneType   `json:"type"`
	Status    string     `json:"status"`
	Config    ZoneConfig `json:"config"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ZoneType defines the type of infrastructure provider.
type ZoneType string

// Zone type constants.
const (
	ZoneTypeProxmox ZoneType = "proxmox"
	ZoneTypeGCP     ZoneType = "gcp"
	ZoneTypeAWS     ZoneType = "aws"
	ZoneTypeAzure   ZoneType = "azure"
)

// IsValid checks if the zone type is valid.
func (t ZoneType) IsValid() bool {
	switch t {
	case ZoneTypeProxmox, ZoneTypeGCP, ZoneTypeAWS, ZoneTypeAzure:
		return true
	default:
		return false
	}
}

// ZoneConfig holds provider-specific configuration.
type ZoneConfig struct {
	Endpoint  string `json:"endpoint,omitempty"`
	Username  string `json:"username,omitempty"`
	Project   string `json:"project,omitempty"`
	Region    string `json:"region,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
}

// ZoneCreateRequest represents a request to create a new zone.
type ZoneCreateRequest struct {
	Name   string     `json:"name" binding:"required"`
	Type   ZoneType   `json:"type" binding:"required"`
	Config ZoneConfig `json:"config"`
}

// ZoneUpdateRequest represents a request to update a zone.
type ZoneUpdateRequest struct {
	Name   *string     `json:"name,omitempty"`
	Config *ZoneConfig `json:"config,omitempty"`
}
