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
	q, err := parseQubeOutput(jsonOut, qubeName)
	if err != nil {
		return "", err
	}
	return q.Status, nil
}

// parseQubeAddress extracts a single qube's IP address from the same output.
//
// Terraform has always emitted this field and nothing read it back, so
// qubes.ip_address stayed empty for every qube ever created. That is not
// cosmetic: the agent listens on the qube's OWN address, so with no address
// recorded there is nothing for a health probe to dial and every agent looks
// equally unreachable whether it is running or not.
func parseQubeAddress(jsonOut, qubeName string) (string, error) {
	q, err := parseQubeOutput(jsonOut, qubeName)
	if err != nil {
		return "", err
	}
	return q.IPAddress, nil
}

// parseQubeOutput decodes the remote_qubes map and picks out one qube.
func parseQubeOutput(jsonOut, qubeName string) (remoteQubeOutput, error) {
	var m map[string]remoteQubeOutput
	if err := json.Unmarshal([]byte(jsonOut), &m); err != nil {
		return remoteQubeOutput{}, fmt.Errorf("parse terraform output: %w", err)
	}
	q, ok := m[qubeName]
	if !ok {
		return remoteQubeOutput{}, fmt.Errorf("qube %q not found in terraform output", qubeName)
	}
	return q, nil
}
