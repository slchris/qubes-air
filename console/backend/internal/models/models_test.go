package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestZoneType_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		zoneType ZoneType
		want     bool
	}{
		{"proxmox", ZoneTypeProxmox, true},
		{"gcp", ZoneTypeGCP, true},
		{"aws", ZoneTypeAWS, true},
		{"azure", ZoneTypeAzure, true},
		{"invalid", ZoneType("invalid"), false},
		{"empty", ZoneType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.zoneType.IsValid()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestQubeType_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		qubeType QubeType
		want     bool
	}{
		{"app", QubeTypeApp, true},
		{"work", QubeTypeWork, true},
		{"dev", QubeTypeDev, true},
		{"gpu", QubeTypeGPU, true},
		{"disp", QubeTypeDisp, true},
		{"sys", QubeTypeSys, true},
		{"invalid", QubeType("invalid"), false},
		{"empty", QubeType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.qubeType.IsValid()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestQubeStatus_IsValid(t *testing.T) {
	tests := []struct {
		name   string
		status QubeStatus
		want   bool
	}{
		{"pending", QubeStatusPending, true},
		{"creating", QubeStatusCreating, true},
		{"running", QubeStatusRunning, true},
		{"stopped", QubeStatusStopped, true},
		{"error", QubeStatusError, true},
		{"invalid", QubeStatus("invalid"), false},
		{"empty", QubeStatus(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.status.IsValid()
			assert.Equal(t, tt.want, got)
		})
	}
}
