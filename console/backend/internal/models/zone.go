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
// ProxmoxZoneConfig carries the settings only a Proxmox zone needs. It lives in
// its own struct so the shared ZoneConfig does not accumulate fields that are
// meaningless for GCP or AWS.
//
// These are zone-level DEFAULTS. A qube may pin its own node (see
// QubeSpec.Node); the rest are properties of the cluster, not of one qube.
type ProxmoxZoneConfig struct {
	// Node is the default node to place qubes on. Empty means "any node",
	// which is only safe when the datastore is shared (Ceph/NFS) — with
	// node-local storage a template cannot be cloned across nodes.
	Node string `json:"node,omitempty"`
	// DatastoreID holds VM disks, e.g. "ceph-pve" (shared) or "local-lvm"
	// (node-local).
	DatastoreID string `json:"datastore_id,omitempty"`
	// NetworkBridge is the bridge new VMs attach to, e.g. "vmbr0".
	NetworkBridge string `json:"network_bridge,omitempty"`
	// TemplateVMID is the cloud-init template VM to clone. Its boot disk must
	// be on scsi0 and it must have a cloud-init drive and qemu-guest-agent, or
	// terraform will wait for an IP that never arrives.
	TemplateVMID int `json:"template_vm_id,omitempty"`
	// TemplateNode is the node the template VM lives on.
	//
	// Distinct from Node, which is where new qubes RUN. The clone API must be
	// called on the template's own node — calling it elsewhere returns "unable
	// to find configuration file for VM <id>". They may differ only when the
	// template's disks are on shared storage; qemu-server allows the clone
	// target parameter only in that case.
	TemplateNode string `json:"template_node,omitempty"`
	// SSHPublicKeys are injected by cloud-init. PUBLIC keys only — a private
	// key must never reach this struct, which is persisted and API-visible.
	SSHPublicKeys []string `json:"ssh_public_keys,omitempty"`
	// CredentialID references an entry in the encrypted credential store
	// holding this cluster's API token (or username/password).
	//
	// The secret is deliberately NOT stored here: ZoneConfig is returned by the
	// zones API in cleartext. Keeping only the reference means the secret can be
	// rotated and audited in one place, and never leaves the credential store
	// except to the components that must call the cluster.
	CredentialID string `json:"credential_id,omitempty"`
}

type ZoneConfig struct {
	Endpoint  string `json:"endpoint,omitempty"`
	Username  string `json:"username,omitempty"`
	Project   string `json:"project,omitempty"`
	Region    string `json:"region,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	// Proxmox is set for zones of type proxmox. The column is already JSON, so
	// adding this needs no migration.
	Proxmox *ProxmoxZoneConfig `json:"proxmox,omitempty"`
	// GCP is set for zones of type gcp. Same reasoning as Proxmox: the shared
	// ZoneConfig does not accumulate fields that are meaningless elsewhere.
	GCP *GCPZoneConfig `json:"gcp,omitempty"`
}

// GCPZoneConfig holds what a GCP zone needs beyond project/region.
//
// Deliberately mirrors ProxmoxZoneConfig: placement defaults plus a reference
// into the credential store, never the secret itself — ZoneConfig is returned
// by the zones API in cleartext.
type GCPZoneConfig struct {
	// Zone is the compute zone, e.g. "asia-east1-b". Instances and their data
	// disks must share one, or the disk cannot be attached.
	Zone string `json:"zone,omitempty"`
	// SourceImage is the boot image, e.g. "debian-cloud/debian-12". GCP has no
	// "clone a template VM" concept, so this replaces Proxmox's template_vm_id.
	SourceImage string `json:"source_image,omitempty"`
	// IdentityBucket is a PRIVATE GCS bucket the per-qube agent identity is
	// delivered through. It cannot go in instance metadata: metadata is a
	// resource attribute, so terraform would write the agent's private key into
	// state in plaintext.
	IdentityBucket string `json:"identity_bucket,omitempty"`
	// ServiceAccountEmail is the instance's service account; it needs read
	// access to IdentityBucket or the VM cannot fetch its own identity.
	ServiceAccountEmail string `json:"service_account_email,omitempty"`
	Network             string `json:"network,omitempty"`
	Subnetwork          string `json:"subnetwork,omitempty"`
	// AssignPublicIP exposes the agent's mTLS port to the internet. Left false,
	// the console is expected to reach it over a private path.
	AssignPublicIP bool `json:"assign_public_ip,omitempty"`
	// CredentialID references the encrypted store entry holding the service
	// account key JSON.
	CredentialID string `json:"credential_id,omitempty"`
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
