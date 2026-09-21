package proxmox

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
)

// IdentityResolver reports where a qube's rendered agent identity lives.
// service.CertIssuer satisfies it structurally.
type IdentityResolver interface {
	// IdentityPath is a local file the adapter uploads to the node over SSH.
	IdentityPath(qubeName string) string
	// IdentityVolumeID is a Proxmox volume id of a snippet already on shared
	// storage, which the adapter references instead of uploading.
	IdentityVolumeID(qubeName string) string
}

// Options configures the parts of the adapter that are not cluster
// credentials.
type Options struct {
	// SSH is required to deliver a cloud-init snippet to a node's local store.
	SSH *SSHConfig
	// Identity resolves a qube's cloud-init identity. Nil disables identity
	// delivery (used by tests and by consoles that do not issue identities).
	Identity IdentityResolver
	// SnippetDatastore is the snippets-capable datastore for the uploaded file.
	// Default "local" (the node-local dir store).
	SnippetDatastore string
	// TaskPoll is the poll interval for asynchronous PVE tasks. Default 2s.
	TaskPoll time.Duration
	// Logf receives progress lines for the job log. Optional.
	Logf func(format string, args ...any)
}

// Adapter implements provider.Adapter against one Proxmox cluster.
type Adapter struct {
	client *Client
	opts   Options
}

// defaultDataDiskGB is used when a qube requests no explicit data disk size.
// A zero-sized disk is not a meaningful request.
const defaultDataDiskGB = 10

// PVE VM/task status words the adapter compares against. The API returns these
// bare strings; naming them keeps the running/stopped checks consistent and
// reviewable in one place.
const (
	vmStatusRunning = "running"
	vmStatusStopped = "stopped"
)

// New builds an Adapter.
func New(cfg Config, opts Options) (*Adapter, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	if opts.SSH != nil {
		if _, err := nodeSSHConfig(*opts.SSH); err != nil {
			return nil, err
		}
	}
	if opts.SnippetDatastore == "" {
		opts.SnippetDatastore = "local"
	}
	if opts.TaskPoll <= 0 {
		opts.TaskPoll = 2 * time.Second
	}
	return &Adapter{client: client, opts: opts}, nil
}

func (a *Adapter) logf(ctx context.Context, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if a.opts.Logf != nil {
		a.opts.Logf("%s", line)
	}
	if w := provider.LogWriterFrom(ctx); w != nil {
		fmt.Fprintln(w, line)
	}
}

// zoneConfig returns the Proxmox-specific zone settings, or an error naming the
// zone when they are absent.
func zoneConfig(zone *models.Zone) (*models.ProxmoxZoneConfig, error) {
	if zone == nil {
		return nil, errors.New("proxmox: zone is nil")
	}
	if zone.Config.Proxmox == nil {
		return nil, fmt.Errorf("proxmox: zone %q has no proxmox config", zone.Name)
	}
	return zone.Config.Proxmox, nil
}

// resolveNode picks the node a qube runs on: its own pin, else the zone default.
func resolveNode(q *models.Qube, pc *models.ProxmoxZoneConfig) (string, error) {
	node := strings.TrimSpace(q.Spec.Node)
	if node == "" {
		node = strings.TrimSpace(pc.Node)
	}
	if node == "" {
		return "", fmt.Errorf("proxmox: zone has no default node and qube %q pins none", q.Name)
	}
	return node, nil
}

// nextID allocates a cluster-wide VMID.
func (a *Adapter) nextID(ctx context.Context) (int, error) {
	var raw string
	if err := a.client.get(ctx, "/api2/json/cluster/nextid", &raw); err != nil {
		return 0, err
	}
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("proxmox: unusable nextid %q", raw)
	}
	return id, nil
}

// vmStatus is the subset of a VM's status this adapter reads.
type vmStatus struct {
	Status string `json:"status"`
	VMID   int    `json:"vmid"`
}

// vmExists reports whether a VM id is present on a node.
func (a *Adapter) vmExists(ctx context.Context, node string, vmid int) (bool, error) {
	var st vmStatus
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/current", url.PathEscape(node), vmid)
	err := a.client.get(ctx, path, &st)
	if IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// diskVolume reads the volume id of a disk from a VM's config, ignoring its
// options (",discard=on,size=...").
func (a *Adapter) diskVolume(ctx context.Context, node string, vmid int, disk string) (string, error) {
	var cfg map[string]any
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	if err := a.client.get(ctx, path, &cfg); err != nil {
		return "", err
	}
	raw, _ := cfg[disk].(string)
	if raw == "" {
		return "", nil
	}
	return strings.SplitN(raw, ",", 2)[0], nil
}

// sizeRE extracts a "size=<n>G" option from a PVE disk config line.
var sizeRE = regexp.MustCompile(`(?:^|,)size=([0-9]+)G(?:,|$)`)

// growDisk grows a disk if wantGB exceeds its current size. PVE cannot shrink,
// so a smaller request is a no-op rather than an error.
func (a *Adapter) growDisk(ctx context.Context, node string, vmid int, disk string, wantGB int) error {
	var cfg map[string]any
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	if err := a.client.get(ctx, path, &cfg); err != nil {
		return err
	}
	raw, _ := cfg[disk].(string)
	m := sizeRE.FindStringSubmatch(raw)
	if m == nil {
		// No explicit size (rare): the template's size applies and PVE resizing
		// a disk without a recorded size is not something to guess at.
		return nil
	}
	have, _ := strconv.Atoi(m[1])
	if wantGB <= have {
		return nil
	}
	form := url.Values{}
	form.Set("disk", disk)
	form.Set("size", strconv.Itoa(wantGB)+"G")
	a.logf(ctx, "grow %s disk %s from %dG to %dG", disk, disk, have, wantGB)
	return a.client.put(ctx, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/resize", url.PathEscape(node), vmid), form, nil)
}

// postTask issues a POST that returns a task and waits for it.
func (a *Adapter) postTask(ctx context.Context, taskNode, path string, form url.Values) error {
	var upid string
	if err := a.client.post(ctx, path, form, &upid); err != nil {
		return err
	}
	a.logf(ctx, "waiting on task %s", upid)
	return a.client.WaitTask(ctx, taskNode, upid, a.opts.TaskPoll)
}

// deleteTask issues a DELETE that returns a task and waits for it.
func (a *Adapter) deleteTask(ctx context.Context, taskNode, path string) error {
	var upid string
	if err := a.client.delete(ctx, path, &upid); err != nil {
		return err
	}
	a.logf(ctx, "waiting on task %s", upid)
	return a.client.WaitTask(ctx, taskNode, upid, a.opts.TaskPoll)
}

// EnsureStorage creates the stopped data-disk holder and its persistent disk.
func (a *Adapter) EnsureStorage(ctx context.Context, q *models.Qube, zone *models.Zone, in provider.Infra) (provider.Infra, error) {
	pc, err := zoneConfig(zone)
	if err != nil {
		return in, err
	}
	node, err := resolveNode(q, pc)
	if err != nil {
		return in, err
	}
	if pc.DatastoreID == "" {
		return in, fmt.Errorf("proxmox: zone %q has no datastore_id", zone.Name)
	}
	if strings.TrimSpace(pc.NetworkBridge) == "" {
		return in, fmt.Errorf("proxmox: zone %q has no network_bridge", zone.Name)
	}
	dataGB := q.Spec.DataDiskGB
	if dataGB <= 0 {
		dataGB = defaultDataDiskGB
	}

	if in.Node != "" && in.Node != node {
		return in, errors.New("proxmox: recorded node differs from requested placement; explicit reconciliation required")
	}
	in.QubeID = q.ID
	in.Provider = string(models.ZoneTypeProxmox)
	in.Node = node

	if in.StorageVMID != 0 {
		adopted, handled, err := a.adoptStorage(ctx, q, node, dataGB, in)
		if err != nil {
			return in, err
		}
		if handled {
			return adopted, nil
		}
	}
	return a.createStorage(ctx, pc, node, q, dataGB, in)
}

// adoptStorage returns the recorded storage VM when it still exists, growing its
// disk if the request asks for more. handled is false when the VM is gone, so
// the caller recreates it.
func (a *Adapter) adoptStorage(ctx context.Context, q *models.Qube, node string, dataGB int, in provider.Infra) (provider.Infra, bool, error) {
	exists, err := a.vmExists(ctx, node, in.StorageVMID)
	if err != nil {
		return in, false, err
	}
	if !exists {
		if in.DataVolume != "" {
			return in, false, errors.New("proxmox: recorded storage holder missing; refusing to replace persistent data")
		}
		return in, false, nil // retry the same reserved identity
	}
	if _, err := a.ownedConfig(ctx, q, node, in.StorageVMID, "storage"); err != nil {
		return in, false, err
	}
	vol, err := a.diskVolume(ctx, node, in.StorageVMID, "scsi0")
	if err != nil {
		return in, false, err
	}
	if vol == "" || (in.DataVolume != "" && in.DataVolume != vol) {
		return in, false, errors.New("proxmox: stored data volume missing or changed; refusing replacement")
	}
	in.DataVolume = vol
	if err := a.growDisk(ctx, node, in.StorageVMID, "scsi0", dataGB); err != nil {
		return in, false, err
	}
	return in, true, nil
}

// createStorage creates the holder VM and records its disk volume.
func (a *Adapter) createStorage(ctx context.Context, pc *models.ProxmoxZoneConfig, node string, q *models.Qube, dataGB int, in provider.Infra) (provider.Infra, error) {
	vmid := in.StorageVMID
	if vmid == 0 {
		var err error
		vmid, err = a.nextID(ctx)
		if err != nil {
			return in, err
		}
	}
	in.StorageVMID = vmid
	if err := provider.Checkpoint(ctx, in); err != nil {
		return in, err
	}
	form := url.Values{}
	form.Set("vmid", strconv.Itoa(vmid))
	form.Set("name", q.Name+"-storage")
	form.Set("description", ownerMarker(q, "storage"))
	form.Set("cores", "1")
	form.Set("memory", "512")
	form.Set("ostype", "l26")
	form.Set("scsihw", "virtio-scsi-single")
	form.Set("net0", "virtio,bridge="+pc.NetworkBridge)
	form.Set("scsi0", fmt.Sprintf("%s:%d,discard=on,iothread=1", pc.DatastoreID, dataGB))
	form.Set("onboot", "0")
	form.Set("tags", "qubes-air;storage;"+string(q.Type))

	a.logf(ctx, "creating storage VM %d on %s (%s %dG)", vmid, node, pc.DatastoreID, dataGB)
	if err := a.postTask(ctx, node, fmt.Sprintf("/api2/json/nodes/%s/qemu", url.PathEscape(node)), form); err != nil {
		return in, err
	}
	vol, err := a.diskVolume(ctx, node, vmid, "scsi0")
	if err != nil {
		return in, err
	}
	in.StorageVMID = vmid
	in.DataVolume = vol
	return in, nil
}

// EnsureCompute creates (or adopts) the ephemeral compute instance, attaches the
// recorded data disk, delivers the identity, and starts it.
func (a *Adapter) EnsureCompute(ctx context.Context, q *models.Qube, zone *models.Zone, in provider.Infra) (provider.Infra, error) {
	pc, err := zoneConfig(zone)
	if err != nil {
		return in, err
	}
	node, err := resolveNode(q, pc)
	if err != nil {
		return in, err
	}
	if pc.TemplateVMID <= 0 {
		return in, fmt.Errorf("proxmox: zone %q has no template_vm_id", zone.Name)
	}
	if in.DataVolume == "" {
		return in, fmt.Errorf("proxmox: qube %q has no recorded data volume; ensure storage first", q.Name)
	}
	if in.Node != "" && in.Node != node {
		return in, errors.New("proxmox: recorded node differs from requested placement; explicit reconciliation required")
	}
	in.QubeID = q.ID
	in.Provider = string(models.ZoneTypeProxmox)
	in.Node = node

	if in.ComputeVMID != 0 {
		adopted, handled, err := a.adoptCompute(ctx, pc, node, q, in)
		if err != nil || handled {
			return adopted, err
		}
	}

	return a.createCompute(ctx, pc, node, q, in)
}

// cloneTemplate clones the zone template onto the target node and waits.
func (a *Adapter) cloneTemplate(ctx context.Context, pc *models.ProxmoxZoneConfig, node string, vmid int, q *models.Qube) error {
	templateNode := strings.TrimSpace(pc.TemplateNode)
	if templateNode == "" {
		templateNode = node
	}
	form := url.Values{}
	form.Set("newid", strconv.Itoa(vmid))
	form.Set("target", node)
	form.Set("full", "1")
	form.Set("name", q.Name)
	form.Set("description", ownerMarker(q, "compute"))

	a.logf(ctx, "cloning template %d from %s to %d on %s", pc.TemplateVMID, templateNode, vmid, node)
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/clone", url.PathEscape(templateNode), pc.TemplateVMID)
	return a.postTask(ctx, templateNode, path, form)
}

// configureCompute applies the compute VM's hardware and cloud-init settings and
// grows its root disk when the qube asks for a larger one than the template.
func (a *Adapter) configureCompute(ctx context.Context, pc *models.ProxmoxZoneConfig, node string, vmid int, q *models.Qube, in *provider.Infra, existingIP string) error {
	cicustom, err := a.identityCicustom(ctx, node, q, in)
	if err != nil {
		return err
	}

	form := url.Values{}
	form.Set("ostype", "l26")
	form.Set("cpu", "x86-64-v2-AES")
	form.Set("agent", "enabled=1")
	form.Set("onboot", "0")
	form.Set("net0", "virtio,bridge="+pc.NetworkBridge)
	form.Set("scsi1", in.DataVolume)
	form.Set("tags", "qubes-air;compute;"+string(q.Type))
	if q.Spec.VCPU > 0 {
		form.Set("cores", strconv.Itoa(q.Spec.VCPU))
	}
	if q.Spec.Memory > 0 {
		form.Set("memory", strconv.Itoa(q.Spec.Memory))
	}
	if cicustom != "" {
		form.Set("cicustom", "user="+cicustom)
		form.Set("ciuser", "qubes")
		ipcfg := existingIP
		if ipcfg == "" {
			var err error
			ipcfg, err = a.computeIPConfig(ctx, pc, node, q.ID)
			if err != nil {
				return err
			}
		}
		form.Set("ipconfig0", ipcfg)
	}
	if len(pc.SSHPublicKeys) > 0 {
		form.Set("sshkeys", strings.Join(pc.SSHPublicKeys, "\n"))
	}

	a.logf(ctx, "configuring compute VM %d", vmid)
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	if err := a.client.post(ctx, path, form, nil); err != nil {
		return err
	}
	if q.Spec.Disk > 0 {
		if err := a.growDisk(ctx, node, vmid, "scsi0", q.Spec.Disk); err != nil {
			return err
		}
	}
	return nil
}

// computeIPConfig returns the ipconfig0 value for a new compute instance: a
// static address from the zone's pool, or DHCP when no pool is configured.
//
// DHCP stays the default because it is right on a network whose DHCP server
// hands out unique leases. A static pool is for the network where it does not:
// there the console would record an address the VM never won (or that another
// host already holds) and the agent would be unreachable with every status
// still reading "running".
func (a *Adapter) computeIPConfig(ctx context.Context, pc *models.ProxmoxZoneConfig, node, qID string) (string, error) {
	pool := strings.TrimSpace(pc.IPPool)
	if pool == "" {
		return "ip=dhcp", nil
	}
	if strings.TrimSpace(pc.Gateway) == "" {
		return "", fmt.Errorf("proxmox: zone has ip_pool %q but no gateway", pool)
	}
	prefix, err := netip.ParsePrefix(pool)
	if err != nil {
		return "", fmt.Errorf("proxmox: bad ip_pool %q: %w", pool, err)
	}

	var probeErr error
	isFree := func(addr netip.Addr) bool {
		free, err := a.addressFree(ctx, node, addr)
		if err != nil {
			if probeErr == nil {
				probeErr = err
			}
			// Treat an unprobeable address as taken: handing out an address we
			// could not check is the collision this pool exists to prevent.
			return false
		}
		return free
	}
	addr, err := allocateIP(pool, qID, isFree)
	if err != nil {
		if probeErr != nil {
			return "", fmt.Errorf("%w (address probe failed: %v)", err, probeErr)
		}
		return "", err
	}
	a.logf(ctx, "assigning static address %s (pool %s, gateway %s)", addr, pool, pc.Gateway)
	return ipconfig0(addr, prefix.Bits(), pc.Gateway), nil
}

// addressFree reports whether addr is unused on the node's bridge.
//
// The check runs from the node, not the console: the console may reach the VM
// subnet by a different path than the VMs use, and the ARP answer is only
// meaningful where the bridge is. ping triggers the ARP and the neighbor entry
// is what is read, so a host that blocks ICMP but answers ARP still counts as
// in use.
func (a *Adapter) addressFree(ctx context.Context, node string, addr netip.Addr) (bool, error) {
	if a.opts.SSH == nil || a.opts.SSH.PrivateKey == "" {
		return false, errors.New("proxmox: ip_pool requires an SSH key to probe addresses")
	}
	// addr was parsed by netip, so interpolating it here cannot inject a shell
	// command.
	ip := addr.String()
	cmd := "ip neigh flush " + ip + " 2>/dev/null; ping -c1 -W1 " + ip + " >/dev/null 2>&1; " +
		"ip neigh show " + ip + " | grep -q lladdr && echo USED || echo FREE"
	out, err := runSSH(ctx, a.nodeAddress(ctx, node), *a.opts.SSH, cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "FREE", nil
}

// nodeAddress resolves a PVE node's management IP for SSH.
//
// The API addresses a node by its short name, but SSH must dial an address the
// console can resolve: the console's split-horizon resolver does not know node
// short names, so `ssh infra-node4` fails with "no such host" even though the
// cluster is reachable by IP. Falls back to the node name only if the lookup
// fails, so the error still names the node.
func (a *Adapter) nodeAddress(ctx context.Context, node string) string {
	var entries []struct {
		Type string `json:"type"`
		Name string `json:"name"`
		IP   string `json:"ip"`
	}
	if err := a.client.get(ctx, "/api2/json/cluster/status", &entries); err != nil {
		a.logf(ctx, "could not resolve an address for node %s: %v", node, err)
		return node
	}
	for _, e := range entries {
		if e.Type == "node" && e.Name == node && e.IP != "" {
			return e.IP
		}
	}
	a.logf(ctx, "node %s has no address in cluster status; dialing the name", node)
	return node
}

// identityCicustom resolves the cloud-init identity reference for a node:
// a shared-storage volume id when present, else a snippet uploaded over SSH.
//
// It returns an error when an identity is expected (a resolver is configured)
// but none is available, rather than starting a VM with no agent — the silent
// failure the bootstrap design exists to remove.
func (a *Adapter) identityCicustom(ctx context.Context, node string, q *models.Qube, in *provider.Infra) (string, error) {
	if a.opts.Identity == nil {
		return "", nil
	}
	if vol := a.opts.Identity.IdentityVolumeID(q.Name); vol != "" {
		in.IdentityVol = vol
		return vol, provider.Checkpoint(ctx, *in)
	}
	path := a.opts.Identity.IdentityPath(q.Name)
	if path == "" {
		return "", fmt.Errorf("proxmox: no agent identity available for %q; refusing to start a qube with no agent", q.Name)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("proxmox: read agent identity for %q: %w", q.Name, err)
	}
	if a.opts.SSH == nil {
		return "", fmt.Errorf("proxmox: identity for %q must be uploaded but no SSH key is configured", q.Name)
	}
	in.IdentityVol = a.opts.SnippetDatastore + ":snippets/" + filepath.Base(path)
	if err := provider.Checkpoint(ctx, *in); err != nil {
		return "", err
	}
	addr := a.nodeAddress(ctx, node)
	if err := uploadSnippet(ctx, addr, *a.opts.SSH, filepath.Base(path), content); err != nil {
		return "", err
	}
	return a.opts.SnippetDatastore + ":snippets/" + filepath.Base(path), nil
}

// ensureStarted starts a VM if it is not already running and waits.
func (a *Adapter) ensureStarted(ctx context.Context, node string, vmid int) error {
	var st vmStatus
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/current", url.PathEscape(node), vmid)
	if err := a.client.get(ctx, path, &st); err != nil {
		return err
	}
	if st.Status == vmStatusRunning {
		return nil
	}
	startPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/start", url.PathEscape(node), vmid)
	return a.postTask(ctx, node, startPath, url.Values{})
}

// StopCompute stops and deletes the compute instance, leaving the persistent
// disk untouched (it is owned by the storage VM, so it survives this delete).
func (a *Adapter) StopCompute(ctx context.Context, q *models.Qube, in provider.Infra) error {
	if in.ComputeVMID == 0 {
		return nil
	}
	if in.Node == "" {
		return errors.New("proxmox: compute identity has no node")
	}
	exists, err := a.vmExists(ctx, in.Node, in.ComputeVMID)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := a.ownedConfig(ctx, q, in.Node, in.ComputeVMID, "compute"); err != nil {
		return err
	}
	if err := a.stopIfRunning(ctx, in.Node, in.ComputeVMID); err != nil {
		return err
	}
	a.logf(ctx, "deleting compute VM %d on %s", in.ComputeVMID, in.Node)
	delPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d", url.PathEscape(in.Node), in.ComputeVMID)
	if err := a.deleteTask(ctx, in.Node, delPath); err != nil {
		return err
	}
	return a.requireVMAbsent(ctx, in.Node, in.ComputeVMID)
}

// stopIfRunning issues a hard stop for a running VM. suspend/release only needs
// the instance gone; a graceful shutdown would block on a guest that may be
// wedged.
func (a *Adapter) stopIfRunning(ctx context.Context, node string, vmid int) error {
	var st vmStatus
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/current", url.PathEscape(node), vmid)
	if err := a.client.get(ctx, path, &st); err != nil {
		return err
	}
	if st.Status != vmStatusRunning {
		return nil
	}
	stopPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/stop", url.PathEscape(node), vmid)
	return a.postTask(ctx, node, stopPath, url.Values{})
}

// DestroyStorage deletes the storage-holder VM and, with it, the persistent
// data disk. Destructive and irreversible; the caller must clear Protected.
func (a *Adapter) DestroyStorage(ctx context.Context, q *models.Qube, in provider.Infra) error {
	if in.Protected {
		return errors.New("proxmox: storage remains protected")
	}
	if in.StorageVMID == 0 {
		return a.cleanupIdentity(ctx, in)
	}
	node := in.Node
	if node == "" {
		return fmt.Errorf("proxmox: cannot destroy storage VM %d without a node", in.StorageVMID)
	}
	exists, err := a.vmExists(ctx, node, in.StorageVMID)
	if err != nil {
		return err
	}
	if !exists {
		return a.cleanupIdentity(ctx, in)
	}

	cfg, err := a.ownedConfig(ctx, q, node, in.StorageVMID, "storage")
	if err != nil {
		return err
	}
	raw, _ := cfg["scsi0"].(string)
	volume := strings.SplitN(raw, ",", 2)[0]
	if volume == "" || (in.DataVolume != "" && in.DataVolume != volume) {
		return errors.New("proxmox: data-volume identity missing or changed; refusing storage deletion")
	}
	if in.DataVolume == "" {
		in.DataVolume = volume
		if err := provider.Checkpoint(ctx, in); err != nil {
			return err
		}
	}
	a.logf(ctx, "DESTROYING storage VM %d on %s (data disk lost)", in.StorageVMID, node)
	delPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d", url.PathEscape(node), in.StorageVMID)
	if err := a.deleteTask(ctx, node, delPath); err != nil {
		return err
	}
	return a.cleanupIdentity(ctx, in)
}

// Describe returns the provider's current view of the qube.
func (a *Adapter) Describe(ctx context.Context, q *models.Qube, in provider.Infra) (provider.Observed, error) {
	if in.ComputeVMID != 0 && in.Node != "" {
		var st vmStatus
		path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/current", url.PathEscape(in.Node), in.ComputeVMID)
		err := a.client.get(ctx, path, &st)
		if err == nil {
			return provider.Observed{
				State:       st.Status,
				IPAddress:   a.agentIP(ctx, in.Node, in.ComputeVMID),
				Node:        in.Node,
				StorageVMID: in.StorageVMID,
				ComputeVMID: in.ComputeVMID,
				DataVolume:  in.DataVolume,
			}, nil
		}
		if !IsNotFound(err) {
			return provider.Observed{}, err
		}
	}
	state := "absent"
	if in.StorageVMID != 0 {
		state = "suspended"
	}
	return provider.Observed{State: state, Node: in.Node, StorageVMID: in.StorageVMID, DataVolume: in.DataVolume}, nil
}

// agentIP asks the guest agent for the VM's first non-loopback IPv4 address.
// Best-effort: an agent that is not yet up yields "", not an error, because
// the address is an observation the health probe will ask for again.
func (a *Adapter) agentIP(ctx context.Context, node string, vmid int) string {
	var payload struct {
		Result []struct {
			Name        string `json:"name"`
			IPAddresses []struct {
				Type    string `json:"ip-address-type"`
				Address string `json:"ip-address"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/network-get-interfaces", url.PathEscape(node), vmid)
	if err := a.client.get(ctx, path, &payload); err != nil {
		a.logf(ctx, "agent network query for %d failed (may not be up yet): %v", vmid, err)
		return ""
	}
	for _, iface := range payload.Result {
		if iface.Name == "lo" {
			continue
		}
		for _, addr := range iface.IPAddresses {
			if addr.Type == "ipv4" && !strings.HasPrefix(addr.Address, "127.") {
				return addr.Address
			}
		}
	}
	return ""
}
