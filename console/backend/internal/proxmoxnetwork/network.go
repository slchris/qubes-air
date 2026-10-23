// Package proxmoxnetwork checks the node network view shared by placement and
// provisioning. It never configures a host or claims upstream connectivity.
package proxmoxnetwork

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	nodeName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)
	bridgeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)
	vlanRange  = regexp.MustCompile(`^([0-9]{1,4})(?:-([0-9]{1,4}))?$`)
)

// Requirement is the network attachment requested by a zone.
type Requirement struct {
	Bridge  string
	VLANTag int
}

// Validate rejects names that could become NIC options and invalid VLAN IDs.
func (r Requirement) Validate() error {
	if !bridgeName.MatchString(r.Bridge) || strings.Contains(r.Bridge, "..") {
		return errors.New("network_bridge must be a 1-15 byte interface name using letters, digits, dot, underscore or hyphen")
	}
	if r.VLANTag < 0 || r.VLANTag > 4094 {
		return errors.New("vlan_tag must be between 0 and 4094")
	}
	return nil
}

// Path validates the provider-supplied node before it enters an API path.
func Path(node string) (string, error) {
	if !nodeName.MatchString(node) || strings.Contains(node, "..") {
		return "", errors.New("invalid Proxmox node name")
	}
	return "/api2/json/nodes/" + node + "/network", nil
}

type networkInterface struct {
	Name      string          `json:"iface"`
	Type      string          `json:"type"`
	Active    json.RawMessage `json:"active"`
	VLANAware json.RawMessage `json:"bridge_vlan_aware"`
	VLANs     string          `json:"bridge_vids"`
}

// Check validates the full API envelope: Proxmox exposes unapplied configuration
// in data and reports its diff in the top-level changes field. Ignoring changes
// would mistake a staged bridge or VLAN setting for the live configuration.
func (r Requirement) Check(raw []byte) error {
	if err := r.Validate(); err != nil {
		return err
	}
	var snapshot struct {
		Data    []networkInterface `json:"data"`
		Changes json.RawMessage    `json:"changes"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return fmt.Errorf("decode node network: %w", err)
	}
	changes := strings.TrimSpace(string(snapshot.Changes))
	if changes != "" && changes != "null" && changes != `""` {
		return errors.New("node has unapplied network changes; apply or revert them before placement")
	}
	var matches []networkInterface
	for _, iface := range snapshot.Data {
		if iface.Name == r.Bridge {
			matches = append(matches, iface)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("bridge %q must appear exactly once in the node network view (found %d)", r.Bridge, len(matches))
	}
	if err := r.checkInterface(matches[0]); err != nil {
		return fmt.Errorf("bridge %q: %w", r.Bridge, err)
	}
	return nil
}

func (r Requirement) checkInterface(iface networkInterface) error {
	if iface.Type != "bridge" && iface.Type != "OVSBridge" {
		return errors.New("interface is not a supported Linux or OVS bridge")
	}
	if !enabled(iface.Active) {
		return errors.New("interface is not active")
	}
	if r.VLANTag == 0 || iface.Type == "OVSBridge" {
		return nil
	}
	if !enabled(iface.VLANAware) {
		return errors.New("tagged Linux bridge must be VLAN aware")
	}
	allowed, err := permitsVLAN(iface.VLANs, r.VLANTag)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("VLAN %d is not in bridge_vids", r.VLANTag)
	}
	return nil
}

func enabled(raw json.RawMessage) bool {
	// PVE serializes boolean fields as JSON booleans or integer 0/1.
	return string(raw) == "1" || string(raw) == "true"
}

func permitsVLAN(spec string, tag int) (bool, error) {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return false, errors.New("tagged Linux bridge has no explicit bridge_vids")
	}
	allowed := false
	for _, field := range fields {
		low, high, err := parseVLANRange(field)
		if err != nil {
			return false, err
		}
		allowed = allowed || (tag >= low && tag <= high)
	}
	return allowed, nil
}

func parseVLANRange(field string) (int, int, error) {
	matches := vlanRange.FindStringSubmatch(field)
	if matches == nil {
		return 0, 0, errors.New("invalid bridge_vids entry")
	}
	low, _ := strconv.Atoi(matches[1])
	high := low
	if matches[2] != "" {
		high, _ = strconv.Atoi(matches[2])
	}
	if low < 1 || high > 4094 || low > high {
		return 0, 0, errors.New("invalid bridge_vids range")
	}
	return low, high, nil
}
