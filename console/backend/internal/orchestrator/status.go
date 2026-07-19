package orchestrator

import (
	"encoding/json"
	"fmt"
)

// remoteQubeOutput mirrors the per-qube shape of the terraform `remote_qubes`
// output (see terraform/outputs.tf). We only decode the fields we need.
type remoteQubeOutput struct {
	Status     string `json:"status"`
	IPAddress  string `json:"ip_address"`
	ComputeUp  bool   `json:"compute_running"`
	DataDiskID string `json:"data_disk_id"`
}

// parseQubeStatus extracts a single qube's status from the JSON produced by
// `terraform output -json remote_qubes`. The output is a map keyed by qube name.
func parseQubeStatus(jsonOut, qubeName string) (string, error) {
	var m map[string]remoteQubeOutput
	if err := json.Unmarshal([]byte(jsonOut), &m); err != nil {
		return "", fmt.Errorf("parse terraform output: %w", err)
	}
	q, ok := m[qubeName]
	if !ok {
		return "", fmt.Errorf("qube %q not found in terraform output", qubeName)
	}
	return q.Status, nil
}
