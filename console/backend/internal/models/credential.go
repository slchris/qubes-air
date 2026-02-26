package models

import "time"

// Credential represents stored credentials.
type Credential struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	Description string     `json:"description"`
	LastUsed    *time.Time `json:"lastUsed"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// CredentialCreateRequest represents a request to create credentials.
type CredentialCreateRequest struct {
	Name        string `json:"name" binding:"required"`
	Type        string `json:"type" binding:"required"`
	Description string `json:"description"`
	Secret      string `json:"secret" binding:"required"`
}

// CredentialUpdateRequest represents a request to update credentials.
type CredentialUpdateRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Secret      *string `json:"secret,omitempty"`
}
