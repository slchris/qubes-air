package models

import "time"

// InfraProvider represents a cloud infrastructure provider.
type InfraProvider struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Type          string      `json:"type"`
	Status        string      `json:"status"`
	Region        string      `json:"region"`
	Config        InfraConfig `json:"config"`
	ResourceCount int         `json:"resourceCount"`
	CreatedAt     time.Time   `json:"createdAt"`
	UpdatedAt     time.Time   `json:"updatedAt"`
}

// InfraConfig holds provider-specific configuration.
type InfraConfig struct {
	Endpoint     string `json:"endpoint,omitempty"`
	CredentialID string `json:"credentialId,omitempty"`
}

// InfraCreateRequest represents a request to create infrastructure.
type InfraCreateRequest struct {
	Name   string      `json:"name" binding:"required"`
	Type   string      `json:"type" binding:"required"`
	Region string      `json:"region"`
	Config InfraConfig `json:"config"`
}

// InfraUpdateRequest represents a request to update infrastructure.
type InfraUpdateRequest struct {
	Name   *string      `json:"name,omitempty"`
	Region *string      `json:"region,omitempty"`
	Config *InfraConfig `json:"config,omitempty"`
}
