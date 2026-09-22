// Package config provides application configuration management.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/keyring"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/qrexec"
	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	Server       ServerConfig       `yaml:"server"`
	Database     DatabaseConfig     `yaml:"database"`
	CORS         CORSConfig         `yaml:"cors"`
	Security     SecurityConfig     `yaml:"security"`
	Auth         AuthConfig         `yaml:"auth"`
	Orchestrator OrchestratorConfig `yaml:"orchestrator"`
	Transport    TransportConfig    `yaml:"transport"`
	QubeSpec     QubeSpecConfig     `yaml:"qube_spec"`

	// LockFile is the file the console takes its exclusive single-instance lock
	// on at startup and holds for its whole lifetime (internal/lockfile). A
	// second console that finds the lock held REFUSES TO START, because the
	// startup path reconciles state that belongs to a live process: it would
	// otherwise mark the running instance's in-flight jobs failed/unknown and
	// overwrite its qube statuses with error.
	//
	// Default: empty, which means "<database.dsn>.lock" — derived from the
	// database rather than configured separately, because the lock only protects
	// anything if "same database" implies "same lock file". Two consoles pointed
	// at one SQLite file then necessarily contend, and an operator who moves the
	// database moves the lock with it. An in-memory database has no file another
	// process could share, so there the derived path is empty and startup skips
	// locking; setting this explicitly is the only way to lock in that case.
	// Env: QUBES_AIR_LOCK_FILE.
	LockFile string `yaml:"lock_file"`
}

// TransportConfig configures the gRPC bidirectional-stream cross-machine
// transport (docs/grpc-transport-design.md, roadmap stage T). When Enabled is
// false (the default) the console uses a no-op transport: cross-machine qrexec
// forwarding is not wired, keeping the console runnable without a remote relay.
//
// When Enabled is true, the local relay dials RemoteEndpoint OUTBOUND over mTLS
// and keeps a long-lived bidi tunnel. Certs are expected to be fetched from
// vault-cloud via qrexec ask and written to the *File paths (in-memory mount);
// this config only points at them, it never holds key material.
type TransportConfig struct {
	// Enabled turns on the real gRPC transport. Env: QUBES_AIR_TRANSPORT_ENABLED.
	Enabled bool `yaml:"enabled"`
	// RemoteEndpoint is the remote Remote-Relay host:port to dial outbound
	// (required when Enabled). Env: QUBES_AIR_TRANSPORT_REMOTE_ENDPOINT.
	RemoteEndpoint string `yaml:"remote_endpoint"`
	// RelayName / RemoteName identify this relay and the target remote
	// (aligns with Qubes RemoteVM remote_name). Env: QUBES_AIR_TRANSPORT_RELAY_NAME
	// / QUBES_AIR_TRANSPORT_REMOTE_NAME.
	RelayName  string `yaml:"relay_name"`
	RemoteName string `yaml:"remote_name"`
	// mTLS material (paths only; provisioned from vault via qrexec ask).
	// Env: QUBES_AIR_TRANSPORT_CA_FILE / _CERT_FILE / _KEY_FILE.
	CAFile   string `yaml:"ca_file"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// KeepAliveSeconds is the heartbeat interval (default 20).
	// Env: QUBES_AIR_TRANSPORT_KEEPALIVE_SECONDS.
	KeepAliveSeconds int `yaml:"keepalive_seconds"`
	// ReconnectMinSeconds / ReconnectMaxSeconds bound the reconnect backoff
	// (defaults 1 / 30). Env: QUBES_AIR_TRANSPORT_RECONNECT_MIN_SECONDS / _MAX_SECONDS.
	ReconnectMinSeconds int `yaml:"reconnect_min_seconds"`
	ReconnectMaxSeconds int `yaml:"reconnect_max_seconds"`
	// ReverseLocalTarget is the local qube a reverse (remote → local) call is
	// delivered to, e.g. "vault-cloud"; dom0 policy C (ask) still gates it. Empty
	// disables reverse calls. Env: QUBES_AIR_TRANSPORT_REVERSE_LOCAL_TARGET.
	ReverseLocalTarget string `yaml:"reverse_local_target"`
	// VaultCerts, when true, fetches mTLS cert/key/CA from vault-cloud via qrexec
	// ask (in memory, not from *File paths). VaultCertName/KeyName/CAName are the
	// credential names. Env: QUBES_AIR_TRANSPORT_VAULT_CERTS (+ _VAULT_CERT_NAME etc).
	VaultCerts    bool   `yaml:"vault_certs"`
	VaultQube     string `yaml:"vault_qube"`
	VaultCertName string `yaml:"vault_cert_name"`
	VaultKeyName  string `yaml:"vault_key_name"`
	VaultCAName   string `yaml:"vault_ca_name"`
}

// OrchestratorConfig configures how start/stop actions map to real
// infrastructure. When Enabled is false (the default) the console uses a no-op
// executor: it flips the DB status without touching a cloud. This keeps the
// console runnable on machines without provider access.
//
// When Enabled is true, start/stop drive the zone's provider adapter directly
// to perform compute/storage separation (suspend/resume). A zone with no
// registered adapter is refused at operation time.
type OrchestratorConfig struct {
	// AgentRevocationURL is reachable from guests and serves signed CA revocations.
	AgentRevocationURL string `yaml:"agent_revocation_url"`
	// Enabled turns on the native provider executor. Env: QUBES_AIR_ORCHESTRATOR_ENABLED.
	Enabled bool `yaml:"enabled"`
	// AgentIdentityDir holds the rendered cloud-init documents that deliver
	// each agent's bootstrap credential — a public CA and a one-shot token,
	// NEVER a private key (docs/bootstrap-design.md §9). Files are still 0600
	// for the token, but a leak here is bounded to one qube's next boot rather
	// than a 90-day identity.
	//
	// With AgentSnippetDatastore set this is instead a MOUNT of the shared
	// storage the PVE nodes read snippets from, and the delivery mode changes
	// accordingly — see that field.
	// Env: QUBES_AIR_AGENT_IDENTITY_DIR.
	AgentIdentityDir string `yaml:"agent_identity_dir"`
	// AgentSnippetDatastore names the Proxmox datastore that AgentIdentityDir
	// is a mount of — e.g. "cephfs". Setting it switches identity delivery from
	// "the console uploads the snippet over SSH" to "the console writes where
	// the nodes already read", which is what takes node root SSH off the
	// provisioning path (docs/bootstrap-design.md §4.4).
	//
	// Empty keeps the SSH upload path, which is the one proven on real
	// hardware. Both are kept because this changes how every qube is
	// provisioned, and a switch that cannot be flipped back would make a bad
	// night unrecoverable.
	//
	// The datastore MUST declare the "snippets" content type. Note that a
	// datastore can appear to work without declaring it — `pvesm path` may
	// still resolve the volume — so verify with a real VM rather than a path
	// lookup.
	//
	// The directory is shared with the hypervisors, so files there are 0644 and
	// confidentiality comes from who can mount the share. Only ever put the
	// bootstrap token and the public CA on this path, never a private key.
	// Env: QUBES_AIR_AGENT_SNIPPET_DATASTORE.
	AgentSnippetDatastore string `yaml:"agent_snippet_datastore"`
	// AgentListen is the address the remote agent binds on (default 0.0.0.0:8443).
	// Env: QUBES_AIR_AGENT_LISTEN.
	AgentListen string `yaml:"agent_listen"`
	// ProxmoxSSHKeyFile is the private key the console uses to SSH into PVE
	// nodes, and ProxmoxSSHUsername the login (default "root").
	//
	// Required for provisioning UNLESS AgentSnippetDatastore is set: uploading a
	// cloud-init snippet writes /var/lib/vz/snippets/ on the node over SSH and
	// the PVE API has no endpoint for it (still true in PVE 9.2 — see
	// docs/bootstrap-design.md §4.1). That snippet carries the per-qube agent
	// identity, so without this every provision fails partway — after the VM
	// has been cloned, which leaves a half-built qube behind.
	//
	// With shared-storage delivery the console writes the snippet itself and the
	// adapter only references it, so nothing in the provisioning path needs to
	// log into a node and this may be left empty.
	//
	// A PATH, not the key itself: the content is read at call time, so it never
	// enters the config surface as a secret. Keeping the path in
	// config means the unit file and any process listing show a filename rather
	// than a private key.
	// Env: QUBES_AIR_PROXMOX_SSH_KEY_FILE / QUBES_AIR_PROXMOX_SSH_USERNAME.
	ProxmoxSSHKnownHostsFile string `yaml:"proxmox_ssh_known_hosts_file"`
	ProxmoxSSHKeyFile        string `yaml:"proxmox_ssh_key_file"`
	ProxmoxSSHUsername       string `yaml:"proxmox_ssh_username"`
	// RegisterRemoteVM makes the console tell dom0 about each provisioned qube,
	// via the qubesair.RegisterRemoteVM qrexec service, so local qubes can
	// address it. Without it the fleet is reachable only from this console.
	//
	// Off by default because it needs the dom0 side installed
	// (mgmt.remotevm.register in qubes-salt-config). With that absent every
	// provision would log a registration failure, which just teaches an
	// operator to ignore the log.
	// Env: QUBES_AIR_REGISTER_REMOTEVM.
	RegisterRemoteVM bool `yaml:"register_remotevm"`
	// EncryptDataDefault is the fleet default for a create request that does not
	// specify encrypt_data: true makes new qubes' data disks LUKS-encrypted
	// unless a request explicitly opts out. Off (the zero value) keeps the
	// historical plaintext default, so a console that never sets it is unchanged
	// — and flipping it back to off is the rollback, no code change needed.
	// Env: QUBES_AIR_ENCRYPT_DATA_DEFAULT.
	EncryptDataDefault bool `yaml:"encrypt_data_default"`
	// AptMirror is the base URL of a Debian mirror for provisioned qubes, e.g.
	// "http://10.31.0.2/debian". Empty leaves the image's own sources alone.
	//
	// Not a tuning knob — it dominates provisioning time. Setting user-data
	// REPLACES a template's vendor data, so whatever mirror the template
	// configured at boot stops being applied and apt falls back to the public
	// Debian redirector. Measured on real hardware: installing two small
	// packages took 857 seconds of a 15-minute provision, 99% of the total, and
	// pushed the apply past the executor's timeout. With a LAN mirror the same
	// step is seconds.
	// Env: QUBES_AIR_APT_MIRROR.
	AptMirror string `yaml:"apt_mirror"`
	// AptSecurityMirror is the security suite's base URL, e.g.
	// "http://10.31.0.2/debian-security". Falls back to AptMirror when empty,
	// which is wrong for most mirrors — Debian serves security from a separate
	// path — so it is worth setting explicitly.
	// Env: QUBES_AIR_APT_SECURITY_MIRROR.
	AptSecurityMirror string `yaml:"apt_security_mirror"`
	// AgentPackageURL is where a booting qube fetches the agent .deb from, e.g.
	// http://10.31.0.2/local/qubes-air/qubes-air-agent_0.1.0_amd64.deb.
	//
	// The agent is deliberately not baked into the VM image, so this URL is the
	// only way the binary ever reaches a remote. Leave it unset and every new
	// qube boots with no agent at all — the failure a real deployment already
	// hit, where "systemctl enable --now qubes-air-agent" no-opped against a
	// unit that was never installed.
	// Env: QUBES_AIR_AGENT_PACKAGE_URL.
	AgentPackageURL string `yaml:"agent_package_url"`
	// AgentPackageSHA256 is the hex digest the downloaded package must match.
	//
	// This is not a corruption check, it is the ONLY integrity control in the
	// delivery chain: the artifact store accepts unauthenticated uploads and
	// serves them over plain HTTP, so anyone on the LAN can replace the .deb.
	// The digest is trustworthy anyway because it travels in the cloud-init
	// identity document, which reaches the guest over a path we control
	// (console -> Proxmox snippet -> cloud-init).
	// Env: QUBES_AIR_AGENT_PACKAGE_SHA256.
	AgentPackageSHA256 string `yaml:"agent_package_sha256"`
	// AgentPackageVersion is the version this console expects to deliver.
	// Advisory only — the digest is what is enforced — but it is what makes a
	// guest log line and an audit trail name a specific build.
	// Env: QUBES_AIR_AGENT_PACKAGE_VERSION.
	AgentPackageVersion string `yaml:"agent_package_version"`
	// AgentAllowedServices is the allowlist written into each new agent's
	// agent.env as QUBESAIR_ALLOW. Defaults to the reachability probe only:
	// Exec, FileCopy and UnlockData run with host root and are opt-in, so a
	// default provision does not hand a fresh host root-capable primitives.
	// Env: QUBES_AIR_AGENT_ALLOWED_SERVICES (comma-separated).
	AgentAllowedServices []string `yaml:"agent_allowed_services"`
	// AgentExecAllow is the executable allowlist delivered to each new agent as
	// QUBESAIR_EXEC_ALLOW: absolute program paths, colon-separated on the wire.
	// Empty DISABLES qubesair.Exec in the guest (the agent refuses an empty
	// allowlist rather than allowing everything), which is the safe default:
	// the service runs commands as root on the remote host.
	// Env: QUBES_AIR_EXEC_ALLOW (colon-separated).
	AgentExecAllow []string `yaml:"agent_exec_allow"`
	// AgentFileCopyRoots is the directory allowlist delivered as
	// QUBESAIR_FILECOPY_ROOTS. Same shape and same empty-means-disabled rule.
	// "/" is refused: it would allow every path on the host.
	// Env: QUBES_AIR_FILECOPY_ROOTS (colon-separated).
	AgentFileCopyRoots []string `yaml:"agent_filecopy_roots"`
	// AgentProbeIntervalSeconds is how often every running qube's agent is
	// re-probed (default 60). Zero or negative DISABLES the periodic reconciler,
	// which leaves agent health frozen at whatever the last probe found.
	//
	// The reconciler is what catches an agent that dies after provisioning —
	// the package removed, the unit crash-looping, the certificate expired.
	// Without it a qube that was healthy once reads healthy forever, which is
	// the same false-green the whole agent-health feature exists to remove.
	// Env: QUBES_AIR_AGENT_PROBE_INTERVAL_SECONDS.
	AgentProbeIntervalSeconds int `yaml:"agent_probe_interval_seconds"`
	// AgentProbeTimeoutSeconds bounds ONE probe end to end (default 10).
	//
	// It must stay well under the interval, and it exists because a qube that
	// accepts TCP and then goes silent would otherwise hold the probe worker
	// forever — this console has already been wedged once by an unbounded wait
	// on infrastructure that never answered.
	// Env: QUBES_AIR_AGENT_PROBE_TIMEOUT_SECONDS.
	AgentProbeTimeoutSeconds int `yaml:"agent_probe_timeout_seconds"`
	// AgentProbeSettleSeconds is how long after a successful provision or resume
	// the console keeps retrying before calling the agent unreachable
	// (default 300).
	//
	// A newly built qube CANNOT answer when the provider returns: cloud-init only
	// starts downloading and installing the agent once the VM has reported its
	// address. Probing once at job completion would therefore mark every healthy
	// qube unreachable. The budget is bounded rather than infinite so that a
	// genuinely broken agent still produces a verdict instead of sitting in
	// "starting" forever, which would hide the failure just as effectively.
	// Env: QUBES_AIR_AGENT_PROBE_SETTLE_SECONDS.
	AgentProbeSettleSeconds int `yaml:"agent_probe_settle_seconds"`
	// AgentCertRenewIntervalSeconds is how often the fleet is checked for agent
	// certificates that are due for renewal (default 3600). Zero or negative
	// DISABLES renewal.
	//
	// Disabling it puts the fleet back on the only other delivery channel there
	// is: cloud-init, which a VM reads once at first boot. Rotating a certificate
	// then means REBUILDING the qube, which turns the certificate lifetime into a
	// fleet rebuild period — and since every certificate in a rollout is issued
	// within the same few minutes, they all expire on the same day.
	// Env: QUBES_AIR_AGENT_CERT_RENEW_INTERVAL_SECONDS.
	AgentCertRenewIntervalSeconds int `yaml:"agent_cert_renew_interval_seconds"`
	// AgentCertRenewThresholdPercent is how much of a certificate's TOTAL
	// lifetime must remain for it to still count as fresh (default 33). Below
	// that it is renewed. Values outside 1..99 fall back to the default.
	//
	// A third of the 90-day agent certificate is roughly a 30-day window — but
	// that width is
	// NOT the margin any individual qube gets. Renewals are jittered forward
	// across the first quarter of the window so a rollout does not renew all at
	// once, so the last qube in the spread begins renewing with substantially
	// less. The console logs the real runway at boot — reason about THAT number
	// before lowering this, not the window width; the console logs
	// the computed value at startup (service.renewalRunway) so it cannot drift
	// away from whatever the threshold is set to here.
	//
	// Sized by how much repeated failure it has to survive rather than by taste:
	// with an hourly sweep and a retry backoff capped at six hours, a qube that
	// is unreachable for a fortnight still gets many tens of attempts in the
	// eight days that remain. It is a PERCENTAGE rather than a fixed number
	// of days so that shortening pki.DefaultAgentCertLifetime shortens the
	// renewal period with it — a fixed 30 days against a 14-day certificate would
	// mean every certificate is born already overdue.
	// Env: QUBES_AIR_AGENT_CERT_RENEW_THRESHOLD_PERCENT.
	AgentCertRenewThresholdPercent int `yaml:"agent_cert_renew_threshold_percent"`
	// AgentBootstrapIntervalSeconds is how often running qubes that hold no
	// certificate are dialed to be issued their first one (default 60). Zero or
	// negative DISABLES bootstrap.
	//
	// Much faster than the renewal sweep, and for the opposite reason. Renewal
	// has weeks of runway; bootstrap is on the path of "I just created a qube
	// and it is not reachable yet", so the interval is roughly how long a new
	// qube spends looking broken before it works. It is cheap to run often:
	// a sweep that finds every qube already certified does one query and stops.
	//
	// Disabling it means a qube provisioned under the token design NEVER
	// receives a certificate — it boots, looks provisioned, and its agent
	// refuses to serve. Nothing else reports that, which is why this is logged
	// loudly at startup rather than left to be discovered.
	// Env: QUBES_AIR_AGENT_BOOTSTRAP_INTERVAL_SECONDS.
	AgentBootstrapIntervalSeconds int `yaml:"agent_bootstrap_interval_seconds"`
	// JobTimeoutSeconds bounds ONE orchestration job end to end (default 2700).
	//
	// A real provision clones a template, installs the agent package, attaches
	// and unlocks the data disk. joblog.go and job_handler.go both document that
	// as 15-25 minutes on hardware, and the package install step alone has been
	// measured at 857 seconds. The bound exists so a wedged provider call cannot
	// hold a worker forever, but a bound shorter than the work it wraps does not
	// fail safely: the job is canceled mid-flight after the VM and its disk
	// already exist, leaving a failed job and half-built infrastructure to
	// reconcile by hand.
	//
	// Zero or negative falls back to the runner's default rather than disabling
	// the bound.
	// Env: QUBES_AIR_ORCHESTRATOR_JOB_TIMEOUT_SECONDS.
	JobTimeoutSeconds int `yaml:"job_timeout_seconds"`
}

// QubeSpecConfig bounds the resource sizes a qube create/update request may
// carry. It is enforced in internal/service before the request is written or sent
// to a provider (production-readiness gap G-H10 / plan item M2-12): a rejected
// spec must never reach an adapter, because a PVE disk cannot be shrunk and the
// size a typo asks for is therefore consumed for as long as that disk exists.
//
// The defaults hold the same numbers as service.DefaultSpecBounds; main.go maps
// one to the other and cmd/server pins them together with a test, so the
// configured path and the service's own fallback cannot drift apart.
//
// 0 is not a size, it means "unset": create replaces a zero with the type default
// (see service.applyDefaultSpec). The minimum therefore applies only to a value
// the caller actually gave.
//
// Every bound is INCLUSIVE. Every pair must satisfy min >= 1 and max >= min;
// Validate refuses one that does not, rather than letting a transposed digit
// either disable the check or refuse every request.
type QubeSpecConfig struct {
	// MinVCPU / MaxVCPU bound spec.vcpu (default 1..32). The upper bound is the
	// UI's own create/edit maximum (console/frontend/src/components/QubeList.svelte
	// :471, :579) and 8x the 4 cores per node of the reference cluster the
	// scheduler was built against.
	// Env: QUBES_AIR_QUBE_SPEC_MIN_VCPU / QUBES_AIR_QUBE_SPEC_MAX_VCPU.
	MinVCPU int `yaml:"min_vcpu"`
	MaxVCPU int `yaml:"max_vcpu"`
	// MinMemoryMB / MaxMemoryMB bound spec.memory, in MB (default 512..262144).
	// The minimum is the UI's own minimum and the size of the console's smallest
	// VM, the data-disk holder. The MAXIMUM is a JUDGEMENT CALL, not a
	// measurement: 256 GiB is 16x the largest built-in type default and has no
	// basis in this repository. It is deliberately a policy bound — lower it if
	// the cluster is smaller.
	// Env: QUBES_AIR_QUBE_SPEC_MIN_MEMORY_MB / QUBES_AIR_QUBE_SPEC_MAX_MEMORY_MB.
	MinMemoryMB int `yaml:"min_memory_mb"`
	MaxMemoryMB int `yaml:"max_memory_mb"`
	// MinDiskGB / MaxDiskGB bound the root disk, in GB (default 10..16384). The
	// minimum is the UI's minimum; the MAXIMUM is a JUDGEMENT CALL — 16 TiB, above
	// any volume a single-operator node presents, and it still refuses the
	// 200000 GiB typo this bound exists for. The datastore's real ceiling is not
	// visible to the API.
	// Env: QUBES_AIR_QUBE_SPEC_MIN_DISK_GB / QUBES_AIR_QUBE_SPEC_MAX_DISK_GB.
	MinDiskGB int `yaml:"min_disk_gb"`
	MaxDiskGB int `yaml:"max_disk_gb"`
	// MinDataDiskGB / MaxDataDiskGB bound spec.data_disk_gb, in GB (default
	// 1..16384). The minimum is the UI's minimum; the maximum shares the root
	// disk's judgement call, and matters more: this is the disk PVE will never
	// shrink.
	// Env: QUBES_AIR_QUBE_SPEC_MIN_DATA_DISK_GB / QUBES_AIR_QUBE_SPEC_MAX_DATA_DISK_GB.
	MinDataDiskGB int `yaml:"min_data_disk_gb"`
	MaxDataDiskGB int `yaml:"max_data_disk_gb"`
	// MinGPUCount / MaxGPUCount bound spec.gpu.count (default 1..8). A JUDGEMENT
	// CALL with no basis in the repository: no provider adapter reads Spec.GPU
	// today, so there is nothing to measure.
	// Env: QUBES_AIR_QUBE_SPEC_MIN_GPU_COUNT / QUBES_AIR_QUBE_SPEC_MAX_GPU_COUNT.
	MinGPUCount int `yaml:"min_gpu_count"`
	MaxGPUCount int `yaml:"max_gpu_count"`
}

// specBoundPair is one dimension's limits, so Validate can walk all of them
// without five copies of the same two checks.
type specBoundPair struct {
	key      string
	min, max int
}

// dimensions lists every bounded dimension, in the order the service checks them.
func (q QubeSpecConfig) dimensions() []specBoundPair {
	return []specBoundPair{
		{"vcpu", q.MinVCPU, q.MaxVCPU},
		{"memory_mb", q.MinMemoryMB, q.MaxMemoryMB},
		{"disk_gb", q.MinDiskGB, q.MaxDiskGB},
		{"data_disk_gb", q.MinDataDiskGB, q.MaxDataDiskGB},
		{"gpu_count", q.MinGPUCount, q.MaxGPUCount},
	}
}

// Validate refuses a bounds set that could not be enforced: a minimum below 1
// bounds nothing, and a maximum below its minimum refuses every request. Both
// look like a working console until someone tries to create a qube, so they are
// caught here instead. The messages name the offending key, because a config typo
// has to be actionable from the startup log alone.
func (q QubeSpecConfig) Validate() error {
	for _, d := range q.dimensions() {
		if d.min < 1 {
			return fmt.Errorf("qube_spec.min_%s (%d) must be at least 1; a non-positive minimum would not bound anything", d.key, d.min)
		}
		if d.max < d.min {
			return fmt.Errorf("qube_spec.max_%s (%d) is below qube_spec.min_%s (%d); every request would be refused", d.key, d.max, d.key, d.min)
		}
	}
	return nil
}

// ServerConfig holds HTTP server configuration.
// DefaultMaxBodyBytes bounds request bodies when nothing else is configured. It
// is generous for the console's JSON payloads (zone/qube specs, settings) while
// stopping a single client from streaming unbounded data into memory.
const DefaultMaxBodyBytes int64 = 1 << 20

type ServerConfig struct {
	Host string    `yaml:"host"`
	Port int       `yaml:"port"`
	Mode string    `yaml:"mode"`
	TLS  TLSConfig `yaml:"tls"`

	// Production makes the console FAIL CLOSED on configuration that is merely
	// warned about in development: an empty API token (auth disabled), the
	// well-known development encryption key, and a wildcard CORS origin. Env:
	// QUBES_AIR_PRODUCTION.
	Production bool `yaml:"production"`

	// MaxBodyBytes caps the size of any request body read by the API. Env:
	// QUBES_AIR_MAX_BODY_BYTES. Zero or negative falls back to
	// DefaultMaxBodyBytes, so an unset value is still bounded.
	MaxBodyBytes int64 `yaml:"max_body_bytes"`

	// RateLimitPerSec and RateLimitBurst bound requests per client. Env:
	// QUBES_AIR_RATE_LIMIT_PER_SEC, QUBES_AIR_RATE_LIMIT_BURST. Non-positive
	// values fall back to the middleware defaults.
	RateLimitPerSec float64 `yaml:"rate_limit_per_sec"`
	RateLimitBurst  int     `yaml:"rate_limit_burst"`

	// WebRoot is the directory holding the built frontend (index.html plus
	// assets/). Empty disables serving it, which is the default: the API is
	// useful on its own and a missing directory must not stop the console from
	// starting.
	//
	// Serving the UI from the same origin as the API is what makes it work
	// without configuration — the frontend calls the relative path /api/v1, so
	// there is no base URL to set and no CORS origin to allow.
	WebRoot string `yaml:"web_root"`
}

// TLSConfig holds TLS/HTTPS configuration.
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// DatabaseConfig holds database configuration.
type DatabaseConfig struct {
	DSN string `yaml:"dsn"`
}

// CORSConfig holds CORS configuration.
type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
	AllowedMethods []string `yaml:"allowed_methods"`
	AllowedHeaders []string `yaml:"allowed_headers"`
}

// SecurityConfig holds security-related configuration.
type SecurityConfig struct {
	// EncryptionKey is the AES-256 key (must be exactly 32 bytes) used to
	// encrypt credential secrets at rest. Leave empty ONLY for local
	// development — a well-known insecure default is used and a warning is
	// logged. Never leave empty in production.
	//
	// This is the single-key form and always maps to key_version 1. For key
	// rotation use EncryptionKeys instead (see below); it takes precedence when
	// non-empty.
	EncryptionKey string `yaml:"encryption_key"`

	// EncryptionKeys is the multi-version key spec used for key rotation, of
	// the form "v1:<32-byte-key>,v2:<32-byte-key>". The highest version is the
	// primary (used to encrypt new secrets); older versions must remain listed
	// until no credential row still references them. When set, it takes
	// precedence over EncryptionKey. See internal/keyring and cmd/rotate-key.
	//
	// Env: QUBES_AIR_ENCRYPTION_KEYS.
	EncryptionKeys string `yaml:"encryption_keys"`
}

// AuthConfig holds API authentication configuration.
type AuthConfig struct {
	// APIToken, when set, is required as a Bearer token on every /api/v1
	// request. When empty, authentication is DISABLED (a warning is logged
	// at startup). Set this before exposing the console beyond localhost.
	//
	// APIToken is the administrator token: it is accepted with control scope,
	// it is not a legacy branch to be retired.
	APIToken string `yaml:"api_token"`

	// Tokens is the scoped-token list: named bearer tokens, each bound to an
	// explicit scope ("read-only" or "control"). A read-only token may only
	// perform the permissive methods (GET/HEAD/OPTIONS); every other method
	// requires a control token (see middleware.RequireControl for the
	// fail-closed method rule).
	Tokens []ScopedToken `yaml:"tokens"`
}

// ScopedToken is one named bearer token and the scope it grants.
//
// The scope is fixed at configuration time and carried by the validated
// request; the API has no endpoint to mint or widen a token, so ".../control"
// cannot be upgraded through the API itself.
type ScopedToken struct {
	// Name is a human label for logs and audit; it carries no authority.
	Name string `yaml:"name"`
	// Token is the expected bearer token value.
	Token string `yaml:"token"`
	// Scope is "read-only" or "control" (config.ScopeReadOnly / ScopeControl).
	Scope string `yaml:"scope"`
	// Zones restricts the token to these zone IDs. Empty means fleet-wide: the
	// token may address every zone and the fleet-only endpoints. A non-empty
	// list is an object-level allowlist enforced by middleware.RequireZones.
	Zones []string `yaml:"zones"`
}

// devEncryptionKey is the well-known insecure key used only when no key is
// configured, to keep local development and tests frictionless.
const devEncryptionKey = "qubes-air-dev-encryption-key32!!" // 32 bytes for AES-256

// IsAuthEnabled reports whether API authentication is enforced.
//
// Enforcement is enabled by the legacy administrator token OR by any scoped
// token. An entry whose Token is empty would fail Validate() before the
// console starts, so a running console never reaches this with a half-written
// token silently ignored.
func (c *Config) IsAuthEnabled() bool {
	if c.Auth.APIToken != "" {
		return true
	}
	for _, t := range c.Auth.Tokens {
		if t.Token != "" {
			return true
		}
	}
	return false
}

// UsesDevEncryptionKey reports whether the insecure development key is in use.
// It is only true when neither the multi-version spec nor a real single key is
// configured (i.e. we fall back to the built-in dev key at version 1).
func (c *Config) UsesDevEncryptionKey() bool {
	if c.Security.EncryptionKeys != "" {
		return false
	}
	return c.Security.EncryptionKey == "" || c.Security.EncryptionKey == devEncryptionKey
}

// EncryptionKeyBytes returns the 32-byte AES key for the single-key form. It
// returns an error if a key is configured but is not exactly 32 bytes, so that
// a misconfiguration fails fast at startup rather than silently falling back to
// the insecure default.
//
// This reflects only Security.EncryptionKey (version 1). When a multi-version
// spec is configured, prefer Keyring(); this method still validates the
// single-key field for callers/tests that use it directly.
func (c *Config) EncryptionKeyBytes() ([]byte, error) {
	if c.Security.EncryptionKey == "" {
		return []byte(devEncryptionKey), nil
	}
	if len(c.Security.EncryptionKey) != 32 {
		return nil, fmt.Errorf(
			"security.encryption_key must be exactly 32 bytes for AES-256, got %d",
			len(c.Security.EncryptionKey),
		)
	}
	return []byte(c.Security.EncryptionKey), nil
}

// Keyring builds the encryption keyring used by the credential repository.
//
// Resolution order:
//  1. If EncryptionKeys (multi-version spec) is set, parse it — the highest
//     version is primary. This is the rotation-capable path.
//  2. Otherwise use the single EncryptionKey (or the dev fallback) at
//     version 1, preserving pre-rotation behavior and legacy rows.
func (c *Config) Keyring() (*keyring.Keyring, error) {
	if c.Security.EncryptionKeys != "" {
		return keyring.ParseSpec(c.Security.EncryptionKeys)
	}
	key, err := c.EncryptionKeyBytes()
	if err != nil {
		return nil, err
	}
	return keyring.NewSingle(key)
}

// DefaultConfig returns configuration with default values.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            8080,
			Mode:            gin.ReleaseMode,
			MaxBodyBytes:    DefaultMaxBodyBytes,
			RateLimitPerSec: 20,
			RateLimitBurst:  40,
			TLS: TLSConfig{
				Enabled:  false,
				CertFile: "",
				KeyFile:  "",
			},
		},
		Database: DatabaseConfig{
			DSN: "./qubes-air.db",
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
			AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Content-Type", "Authorization"},
		},
		Security: SecurityConfig{
			// Empty by default: resolves to the insecure dev key with a
			// startup warning. Configure a real 32-byte key in production.
			EncryptionKey: "",
		},
		Auth: AuthConfig{
			// Empty by default: authentication is disabled with a startup
			// warning. Set an API token before exposing beyond localhost.
			APIToken: "",
		},
		Orchestrator: OrchestratorConfig{
			// Disabled by default: start/stop only flip DB status (no-op
			// executor). Enable to drive the zone's provider adapter.
			Enabled: false,
			// Privileged agent services are opt-in; a fresh qube gets only the
			// reachability probe unless this is widened deliberately.
			AgentAllowedServices: []string{"qubesair.Ping"},
			// Agent probing defaults ON even with orchestration disabled: it
			// reads infrastructure rather than changing it, and a console that
			// only reports agent health when someone remembered to switch it on
			// is a console that reports nothing on the day it matters.
			AgentProbeIntervalSeconds: 60,
			AgentProbeTimeoutSeconds:  10,
			AgentProbeSettleSeconds:   300,
			// Renewal defaults ON for the same reason probing does: a console
			// that only renews when someone remembered to switch it on is a
			// console that has not renewed anything on the day it matters.
			AgentCertRenewIntervalSeconds:  3600,
			AgentCertRenewThresholdPercent: 33,
			AgentBootstrapIntervalSeconds:  60,
			// 45 minutes, against a documented 15-25 minute provision.
			JobTimeoutSeconds: 2700,
		},
		Transport: TransportConfig{
			// Disabled by default: no gRPC transport wired (noop). Enable and
			// set remote_endpoint + mTLS files to drive cross-machine qrexec.
			Enabled:             false,
			KeepAliveSeconds:    20,
			ReconnectMinSeconds: 1,
			ReconnectMaxSeconds: 30,
		},
		// Bounds on every size a create/update request may carry. Same numbers
		// as service.DefaultSpecBounds, which is what an unset/unusable set falls
		// back to; cmd/server's TestSpecBoundsFromConfigMatchesServiceDefaults
		// keeps the two in step. Read the field comments above before changing
		// one: the memory/disk/gpu maxima are policy, not measured limits.
		QubeSpec: QubeSpecConfig{
			MinVCPU:       1,
			MaxVCPU:       32,
			MinMemoryMB:   512,
			MaxMemoryMB:   262144,
			MinDiskGB:     10,
			MaxDiskGB:     16384,
			MinDataDiskGB: 1,
			MaxDataDiskGB: 16384,
			MinGPUCount:   1,
			MaxGPUCount:   8,
		},
	}
}

// Load loads configuration from file and environment variables.
// Environment variables take precedence over file configuration.
func Load(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	if configPath != "" {
		if err := cfg.loadFromFile(configPath); err != nil {
			return nil, fmt.Errorf("failed to load config file: %w", err)
		}
	}

	cfg.loadFromEnv()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// loadFromFile loads configuration from a YAML file.
func (c *Config) loadFromFile(path string) error {
	// Sanitize the path to prevent directory traversal
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath) // #nosec G304 -- config path is provided by trusted application startup flags
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return yaml.Unmarshal(data, c)
}

// loadFromEnv loads configuration from environment variables. Environment
// variables take precedence over file configuration.
//
// One named step per configuration section, so a field's lookup sits with the
// fields it belongs to. The per-field branches have to be counted somewhere;
// the step boundary is what keeps each function under the gocyclo/funlen
// limits without a waiver.
func (c *Config) loadFromEnv() {
	c.loadServerEnv()
	c.loadServerRequestLimitsEnv()
	c.loadStorageEnv()
	c.loadSecurityEnv()
	c.loadOrchestratorEnv()
	c.loadAgentPackageEnv()
	c.loadOrchestratorTimingEnv()
	c.loadAgentRenewalEnv()
	c.loadTransportEnv()
	c.loadTransportVaultEnv()
	c.loadTransportTimingEnv()
	c.loadQubeSpecBoundsFromEnv()
}

// loadServerEnv reads the HTTP listener surface: address, dev mode, the static
// web root, the server certificate pair, production hardening and the browser
// origins the console answers.
func (c *Config) loadServerEnv() {
	if host := os.Getenv("QUBES_AIR_HOST"); host != "" {
		c.Server.Host = host
	}
	if port := os.Getenv("QUBES_AIR_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			c.Server.Port = p
		}
	}
	if mode := os.Getenv("GIN_MODE"); mode != "" {
		c.Server.Mode = mode
	}
	if webRoot := os.Getenv("QUBES_AIR_WEB_ROOT"); webRoot != "" {
		c.Server.WebRoot = webRoot
	}
	if enabled := os.Getenv("QUBES_AIR_TLS_ENABLED"); enabled != "" {
		c.Server.TLS.Enabled = strings.ToLower(enabled) == "true"
	}
	if certFile := os.Getenv("QUBES_AIR_TLS_CERT"); certFile != "" {
		c.Server.TLS.CertFile = certFile
	}
	if keyFile := os.Getenv("QUBES_AIR_TLS_KEY"); keyFile != "" {
		c.Server.TLS.KeyFile = keyFile
	}
	if v := os.Getenv("QUBES_AIR_PRODUCTION"); v != "" {
		c.Server.Production = strings.ToLower(v) == "true"
	}
	if origins := os.Getenv("QUBES_AIR_CORS_ORIGINS"); origins != "" {
		c.CORS.AllowedOrigins = strings.Split(origins, ",")
	}
}

// loadServerRequestLimitsEnv reads the request-size and rate ceilings. Each is
// applied only when it parses to a positive number, so a typo keeps the default
// instead of disabling the limit.
func (c *Config) loadServerRequestLimitsEnv() {
	if v := os.Getenv("QUBES_AIR_MAX_BODY_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			c.Server.MaxBodyBytes = n
		}
	}
	if v := os.Getenv("QUBES_AIR_RATE_LIMIT_PER_SEC"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			c.Server.RateLimitPerSec = n
		}
	}
	if v := os.Getenv("QUBES_AIR_RATE_LIMIT_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Server.RateLimitBurst = n
		}
	}
}

// loadStorageEnv reads where the console keeps state: the database DSN and the
// lock file that keeps two consoles off the same database.
func (c *Config) loadStorageEnv() {
	if dsn := os.Getenv("QUBES_AIR_DATABASE_DSN"); dsn != "" {
		c.Database.DSN = dsn
	}
	if lockFile := os.Getenv("QUBES_AIR_LOCK_FILE"); lockFile != "" {
		c.LockFile = lockFile
	}
}

// loadSecurityEnv reads the secrets: the encryption keyring and the API token.
func (c *Config) loadSecurityEnv() {
	if key := os.Getenv("QUBES_AIR_ENCRYPTION_KEY"); key != "" {
		c.Security.EncryptionKey = key
	}
	if keys := os.Getenv("QUBES_AIR_ENCRYPTION_KEYS"); keys != "" {
		c.Security.EncryptionKeys = keys
	}
	if token := os.Getenv("QUBES_AIR_API_TOKEN"); token != "" {
		c.Auth.APIToken = token
	}
}

// loadOrchestratorEnv reads the orchestrator's own settings: the agent identity
// it talks to, the Proxmox SSH material, and the mirrors and toggles a
// provision uses.
func (c *Config) loadOrchestratorEnv() {
	if enabled := os.Getenv("QUBES_AIR_ORCHESTRATOR_ENABLED"); enabled != "" {
		c.Orchestrator.Enabled = strings.ToLower(enabled) == "true"
	}
	if v := os.Getenv("QUBES_AIR_AGENT_SNIPPET_DATASTORE"); v != "" {
		c.Orchestrator.AgentSnippetDatastore = v
	}
	if dir := os.Getenv("QUBES_AIR_AGENT_IDENTITY_DIR"); dir != "" {
		c.Orchestrator.AgentIdentityDir = dir
	}
	if listen := os.Getenv("QUBES_AIR_AGENT_LISTEN"); listen != "" {
		c.Orchestrator.AgentListen = listen
	}
	if v := os.Getenv("QUBES_AIR_AGENT_REVOCATION_URL"); v != "" {
		c.Orchestrator.AgentRevocationURL = v
	}
	if v := os.Getenv("QUBES_AIR_PROXMOX_SSH_KNOWN_HOSTS_FILE"); v != "" {
		c.Orchestrator.ProxmoxSSHKnownHostsFile = v
	}
	if v := os.Getenv("QUBES_AIR_PROXMOX_SSH_KEY_FILE"); v != "" {
		c.Orchestrator.ProxmoxSSHKeyFile = v
	}
	if v := os.Getenv("QUBES_AIR_PROXMOX_SSH_USERNAME"); v != "" {
		c.Orchestrator.ProxmoxSSHUsername = v
	}
	if v := os.Getenv("QUBES_AIR_REGISTER_REMOTEVM"); v != "" {
		c.Orchestrator.RegisterRemoteVM = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("QUBES_AIR_ENCRYPT_DATA_DEFAULT"); v != "" {
		c.Orchestrator.EncryptDataDefault = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("QUBES_AIR_APT_MIRROR"); v != "" {
		c.Orchestrator.AptMirror = v
	}
	if v := os.Getenv("QUBES_AIR_APT_SECURITY_MIRROR"); v != "" {
		c.Orchestrator.AptSecurityMirror = v
	}
}

// loadAgentPackageEnv reads the agent artifact a provision installs and the
// allowlists the agent runs under.
func (c *Config) loadAgentPackageEnv() {
	if url := os.Getenv("QUBES_AIR_AGENT_PACKAGE_URL"); url != "" {
		c.Orchestrator.AgentPackageURL = url
	}
	if sha := os.Getenv("QUBES_AIR_AGENT_PACKAGE_SHA256"); sha != "" {
		c.Orchestrator.AgentPackageSHA256 = sha
	}
	if v := os.Getenv("QUBES_AIR_AGENT_PACKAGE_VERSION"); v != "" {
		c.Orchestrator.AgentPackageVersion = v
	}
	if v := os.Getenv("QUBES_AIR_AGENT_ALLOWED_SERVICES"); v != "" {
		c.Orchestrator.AgentAllowedServices = splitCSV(v)
	}
	// Colon-separated, matching the agent's own format, so a value pasted from
	// agent.env means the same thing here.
	if v := os.Getenv("QUBES_AIR_EXEC_ALLOW"); v != "" {
		c.Orchestrator.AgentExecAllow = splitColon(v)
	}
	if v := os.Getenv("QUBES_AIR_FILECOPY_ROOTS"); v != "" {
		c.Orchestrator.AgentFileCopyRoots = splitColon(v)
	}
}

// loadOrchestratorTimingEnv reads the probe cadence and the per-job budget.
//
// Parsed with Atoi and applied only on success, matching the transport timings
// in loadTransportTimingEnv. A typo therefore keeps the default rather than
// silently resolving to 0, which for the interval would disable probing
// outright.
func (c *Config) loadOrchestratorTimingEnv() {
	if v := os.Getenv("QUBES_AIR_AGENT_PROBE_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Orchestrator.AgentProbeIntervalSeconds = n
		}
	}
	if v := os.Getenv("QUBES_AIR_AGENT_PROBE_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Orchestrator.AgentProbeTimeoutSeconds = n
		}
	}
	if v := os.Getenv("QUBES_AIR_AGENT_PROBE_SETTLE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Orchestrator.AgentProbeSettleSeconds = n
		}
	}
	if v := os.Getenv("QUBES_AIR_ORCHESTRATOR_JOB_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Orchestrator.JobTimeoutSeconds = n
		}
	}
}

// loadAgentRenewalEnv reads the certificate renewal and bootstrap cadence. Same
// Atoi rule as loadOrchestratorTimingEnv: a typo keeps the default.
func (c *Config) loadAgentRenewalEnv() {
	if v := os.Getenv("QUBES_AIR_AGENT_CERT_RENEW_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Orchestrator.AgentCertRenewIntervalSeconds = n
		}
	}
	if v := os.Getenv("QUBES_AIR_AGENT_CERT_RENEW_THRESHOLD_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Orchestrator.AgentCertRenewThresholdPercent = n
		}
	}
	if v := os.Getenv("QUBES_AIR_AGENT_BOOTSTRAP_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Orchestrator.AgentBootstrapIntervalSeconds = n
		}
	}
}

// loadTransportEnv reads the relay identity and the mTLS material paths.
func (c *Config) loadTransportEnv() {
	if enabled := os.Getenv("QUBES_AIR_TRANSPORT_ENABLED"); enabled != "" {
		c.Transport.Enabled = strings.ToLower(enabled) == "true"
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_REMOTE_ENDPOINT"); v != "" {
		c.Transport.RemoteEndpoint = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_RELAY_NAME"); v != "" {
		c.Transport.RelayName = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_REMOTE_NAME"); v != "" {
		c.Transport.RemoteName = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_CA_FILE"); v != "" {
		c.Transport.CAFile = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_CERT_FILE"); v != "" {
		c.Transport.CertFile = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_KEY_FILE"); v != "" {
		c.Transport.KeyFile = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_REVERSE_LOCAL_TARGET"); v != "" {
		c.Transport.ReverseLocalTarget = v
	}
}

// loadTransportVaultEnv reads the vault-issued certificate names, which replace
// the file paths above when transport.vault_certs is on.
func (c *Config) loadTransportVaultEnv() {
	if v := os.Getenv("QUBES_AIR_TRANSPORT_VAULT_CERTS"); v != "" {
		c.Transport.VaultCerts = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_VAULT_QUBE"); v != "" {
		c.Transport.VaultQube = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_VAULT_CERT_NAME"); v != "" {
		c.Transport.VaultCertName = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_VAULT_KEY_NAME"); v != "" {
		c.Transport.VaultKeyName = v
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_VAULT_CA_NAME"); v != "" {
		c.Transport.VaultCAName = v
	}
}

// loadTransportTimingEnv reads the keepalive interval and the reconnect bounds.
func (c *Config) loadTransportTimingEnv() {
	if v := os.Getenv("QUBES_AIR_TRANSPORT_KEEPALIVE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Transport.KeepAliveSeconds = n
		}
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_RECONNECT_MIN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Transport.ReconnectMinSeconds = n
		}
	}
	if v := os.Getenv("QUBES_AIR_TRANSPORT_RECONNECT_MAX_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Transport.ReconnectMaxSeconds = n
		}
	}
}

// loadQubeSpecBoundsFromEnv reads the qube_spec bounds, the last step of
// loadFromEnv. One lookup per field, each through intFromEnv, so a typo keeps
// the configured default rather than resolving to 0 — which for a minimum
// would drop the lower bound and for a maximum would refuse every request.
func (c *Config) loadQubeSpecBoundsFromEnv() {
	q := &c.QubeSpec
	q.MinVCPU = intFromEnv("QUBES_AIR_QUBE_SPEC_MIN_VCPU", q.MinVCPU)
	q.MaxVCPU = intFromEnv("QUBES_AIR_QUBE_SPEC_MAX_VCPU", q.MaxVCPU)
	q.MinMemoryMB = intFromEnv("QUBES_AIR_QUBE_SPEC_MIN_MEMORY_MB", q.MinMemoryMB)
	q.MaxMemoryMB = intFromEnv("QUBES_AIR_QUBE_SPEC_MAX_MEMORY_MB", q.MaxMemoryMB)
	q.MinDiskGB = intFromEnv("QUBES_AIR_QUBE_SPEC_MIN_DISK_GB", q.MinDiskGB)
	q.MaxDiskGB = intFromEnv("QUBES_AIR_QUBE_SPEC_MAX_DISK_GB", q.MaxDiskGB)
	q.MinDataDiskGB = intFromEnv("QUBES_AIR_QUBE_SPEC_MIN_DATA_DISK_GB", q.MinDataDiskGB)
	q.MaxDataDiskGB = intFromEnv("QUBES_AIR_QUBE_SPEC_MAX_DATA_DISK_GB", q.MaxDataDiskGB)
	q.MinGPUCount = intFromEnv("QUBES_AIR_QUBE_SPEC_MIN_GPU_COUNT", q.MinGPUCount)
	q.MaxGPUCount = intFromEnv("QUBES_AIR_QUBE_SPEC_MAX_GPU_COUNT", q.MaxGPUCount)
}

// intFromEnv parses one integer environment variable, keeping fallback when it is
// unset or unparseable. Same rule as the inline Atoi calls in the load*TimingEnv
// steps: a typo leaves the previous value in place instead of silently becoming 0.
func intFromEnv(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// Validate checks if the configuration is valid.
//
// One named step per area, in the order an operator meets it: how the console
// listens, who may call it, what it may execute, then the secrets and the
// outbound transport. Each step reports the first field that is wrong.
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Server.Port)
	}

	if err := c.validateAuth(); err != nil {
		return err
	}
	if err := validateQrexecAllowlists(c.Orchestrator.AgentExecAllow, c.Orchestrator.AgentFileCopyRoots); err != nil {
		return err
	}
	if err := c.validateServerTLS(); err != nil {
		return err
	}

	// Fail fast on a misconfigured encryption key rather than silently
	// falling back to the insecure default. Building the keyring validates both
	// the single-key and multi-version forms (length, version syntax, primary).
	if _, err := c.Keyring(); err != nil {
		return err
	}

	// A bounds set that cannot be enforced is refused here rather than shipped:
	// min < 1 would drop a lower bound and max < min would refuse every create,
	// and neither is visible until someone tries to provision.
	if err := c.QubeSpec.Validate(); err != nil {
		return err
	}

	if err := c.validateProductionHardening(); err != nil {
		return err
	}

	// Real orchestration no longer requires a terraform working directory. The
	// native provider executor drives each zone's API directly and records
	// resource identities in qube_infra, so the console starts on a machine
	// with no terraform installed; a zone with no registered adapter fails
	// loudly at operation time.

	if err := c.validateAgentArtifacts(); err != nil {
		return err
	}
	if err := c.validateTransport(); err != nil {
		return err
	}

	return nil
}

// validateQrexecAllowlists checks both path allowlists at startup so a typo
// fails here rather than as a refused call inside a guest during a provision.
// The renderer re-checks before writing agent.env; this is the earlier, louder
// gate.
func validateQrexecAllowlists(execAllow, fileCopyRoots []string) error {
	if err := qrexec.ValidatePathAllowlist("agent_exec_allow", execAllow, false); err != nil {
		return err
	}
	return qrexec.ValidatePathAllowlist("agent_filecopy_roots", fileCopyRoots, true)
}

// validateServerTLS requires the certificate pair when TLS is on, and checks
// that both files exist, so a missing file fails at startup instead of on the
// first connection.
func (c *Config) validateServerTLS() error {
	if !c.Server.TLS.Enabled {
		return nil
	}
	if c.Server.TLS.CertFile == "" {
		return fmt.Errorf("TLS enabled but cert_file not specified")
	}
	if c.Server.TLS.KeyFile == "" {
		return fmt.Errorf("TLS enabled but key_file not specified")
	}
	if _, err := os.Stat(c.Server.TLS.CertFile); os.IsNotExist(err) {
		return fmt.Errorf("TLS cert file not found: %s", c.Server.TLS.CertFile)
	}
	if _, err := os.Stat(c.Server.TLS.KeyFile); os.IsNotExist(err) {
		return fmt.Errorf("TLS key file not found: %s", c.Server.TLS.KeyFile)
	}
	return nil
}

// validateProductionHardening refuses the two silent downgrades a networked
// control plane must not run with: the well-known development encryption key
// and a wildcard CORS origin. A warning in a journal is not a control.
func (c *Config) validateProductionHardening() error {
	if !c.Server.Production {
		return nil
	}
	if c.UsesDevEncryptionKey() {
		return fmt.Errorf("server.production is true but the well-known development " +
			"encryption key is in use; set encryption_key/encryption_keys")
	}
	for _, o := range c.CORS.AllowedOrigins {
		if o == "*" {
			return fmt.Errorf("server.production is true but cors.allowed_origins contains \"*\"; " +
				"restrict it to the console's own origins")
		}
	}
	return nil
}

// validateAgentArtifacts checks the two artifacts a provision downloads: the
// revocation endpoint the agent polls and the agent package itself.
//
// A package URL without a digest would have every new qube install, as root,
// whatever the unauthenticated artifact store happened to be serving. Refusing
// at startup is the loud place to catch it: the renderer's own fallback is a
// qube that comes up with no agent, which is only discovered when something
// tries to reach it.
func (c *Config) validateAgentArtifacts() error {
	if c.Orchestrator.Enabled || c.Orchestrator.AgentRevocationURL != "" {
		if err := pki.ValidateRevocationURL(c.Orchestrator.AgentRevocationURL); err != nil {
			return err
		}
	}
	return validateAgentPackage(c.Orchestrator.AgentPackageURL, c.Orchestrator.AgentPackageSHA256)
}

// validateTransport requires the remote endpoint and mTLS material when the
// gRPC transport is on — otherwise the outbound tunnel would fail at runtime.
func (c *Config) validateTransport() error {
	if !c.Transport.Enabled {
		return nil
	}
	if c.Transport.RemoteEndpoint == "" {
		return fmt.Errorf("transport.enabled is true but transport.remote_endpoint is not set")
	}
	// mTLS material is required, from vault (names) or from files.
	if c.Transport.VaultCerts {
		if c.Transport.VaultCertName == "" || c.Transport.VaultKeyName == "" {
			return fmt.Errorf("transport.vault_certs is true but transport.vault_cert_name/vault_key_name are not set")
		}
	} else if c.Transport.CertFile == "" || c.Transport.KeyFile == "" {
		return fmt.Errorf("transport.enabled is true but transport.cert_file/key_file (mTLS) are not set (or set transport.vault_certs)")
	}
	if c.Transport.ReconnectMinSeconds > 0 && c.Transport.ReconnectMaxSeconds > 0 &&
		c.Transport.ReconnectMinSeconds > c.Transport.ReconnectMaxSeconds {
		return fmt.Errorf("transport.reconnect_min_seconds (%d) must not exceed reconnect_max_seconds (%d)",
			c.Transport.ReconnectMinSeconds, c.Transport.ReconnectMaxSeconds)
	}
	return nil
}

// validateTokenZones checks the object-level allowlist of one token. An empty
// list is the fleet-wide spelling; "*" is rejected so the two cannot be
// confused, and a zone ID that is not exactly a configured value (stray
// whitespace, empty entry, duplicate) is refused rather than silently making
// the allowlist match nothing.
func validateTokenZones(i int, t ScopedToken) error {
	seen := make(map[string]bool, len(t.Zones))
	for j, zone := range t.Zones {
		if zone == "" || strings.TrimSpace(zone) != zone {
			return fmt.Errorf("auth.tokens[%d] (%q): zones[%d] must be a non-empty zone id without surrounding whitespace", i, t.Name, j)
		}
		if zone == "*" {
			return fmt.Errorf("auth.tokens[%d] (%q): use an empty zones list for a fleet-wide token, not %q", i, t.Name, zone)
		}
		if seen[zone] {
			return fmt.Errorf("auth.tokens[%d] (%q): duplicate entry in zones: %q", i, t.Name, zone)
		}
		seen[zone] = true
	}
	return nil
}

// validateAuth fails fast on a malformed token list.
//
// A bad scope or an empty token must stop the console at startup rather than
// be silently skipped: a token that is configured but ignored is a permission
// the operator believes exists and an authorization decision made somewhere
// else. Scope values live in middleware (the enforcing layer) so validation
// and enforcement cannot drift apart.
func (c *Config) validateAuth() error {
	for i, t := range c.Auth.Tokens {
		if t.Token == "" {
			return fmt.Errorf("auth.tokens[%d] (%q): token must not be empty", i, t.Name)
		}
		if !middleware.ValidScope(t.Scope) {
			return fmt.Errorf("auth.tokens[%d] (%q): scope must be %q or %q, got %q",
				i, t.Name, middleware.ScopeReadOnly, middleware.ScopeControl, t.Scope)
		}
		if err := validateTokenZones(i, t); err != nil {
			return err
		}
	}
	// Fail closed in production: an empty token disables authentication, which
	// on a networked control plane means anyone who can reach the port can drive
	// the fleet. Development keeps the warning instead.
	if c.Server.Production && c.Auth.APIToken == "" && len(c.Auth.Tokens) == 0 {
		return fmt.Errorf("server.production is true but auth.api_token is empty; " +
			"authentication would be disabled")
	}
	return nil
}

// sha256Hex matches a bare 64-character hex digest, the only form sha256sum(1)
// accepts in the guest's verification step.
var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// validateAgentPackage checks the URL/digest pair before the console can start.
//
// Both halves are checked here rather than only at render time because a
// mistyped digest produces a qube that downloads the right package, fails
// verification and installs nothing — indistinguishable at a glance from a
// network problem, and only visible on the guest's console.
func validateAgentPackage(url, sha string) error {
	if url == "" && sha == "" {
		return nil
	}
	if url == "" {
		return fmt.Errorf("orchestrator.agent_package_sha256 is set but orchestrator.agent_package_url is not")
	}
	if sha == "" {
		return fmt.Errorf(
			"orchestrator.agent_package_url is set but orchestrator.agent_package_sha256 is not: " +
				"the artifact store is unauthenticated plain HTTP, so an unpinned package will not be installed")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("orchestrator.agent_package_url must be an http(s) URL, got %q", url)
	}
	// The URL is interpolated into a shell script in the guest. It is quoted
	// there, so only a quote character can escape it — but rejecting the whole
	// class is cheaper than reasoning about it every time that script changes.
	if strings.ContainsAny(url, "'\"`\\ \t\r\n") {
		return fmt.Errorf("orchestrator.agent_package_url must not contain quotes or whitespace, got %q", url)
	}
	if !sha256Hex.MatchString(sha) {
		return fmt.Errorf("orchestrator.agent_package_sha256 must be 64 hex characters, got %q", sha)
	}
	return nil
}

// Address returns the server listen address.
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

// IsTLSEnabled returns whether TLS is enabled.
func (c *Config) IsTLSEnabled() bool {
	return c.Server.TLS.Enabled
}

// splitCSV trims a comma-separated environment list, dropping empty entries.
// splitColon splits the agent's own list format. An empty entry is kept rather
// than dropped: "a::b" is a typo the validator must reject, not silently repair.
func splitColon(v string) []string {
	return strings.Split(v, ":")
}

func splitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// JobLogDir is where operation output is kept, one file per job.
//
// Derived from the database path rather than configured separately: the DSN is
// already the answer to "where does this console keep things it must not lose",
// and a second setting for the same question is a second thing to get wrong.
// An operator who moves the database gets the logs moved with it.
//
// Empty when the database is in-memory or unset — there is nowhere sensible to
// put files then, and the log endpoint reports that plainly rather than writing
// into the working directory of whoever started the process.
func (c *Config) JobLogDir() string {
	dsn := databaseFilePath(c.Database.DSN)
	if dsn == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dsn), "job-logs")
}

// LockFilePath is the file the console's exclusive single-instance lock is
// taken on, and empty when there is nothing to exclude (see LockFile).
//
// The default is derived from the database path — "<database.dsn>.lock" — so
// that two consoles sharing a database necessarily contend for one lock. A
// fixed name next to the database would instead make two consoles with
// DIFFERENT databases in one directory refuse to run beside each other.
func (c *Config) LockFilePath() string {
	if c.LockFile != "" {
		return c.LockFile
	}
	dsn := databaseFilePath(c.Database.DSN)
	if dsn == "" {
		return ""
	}
	return dsn + ".lock"
}

// databaseFilePath returns the filesystem path behind the configured DSN, or
// "" when there is none: an in-memory database, or no DSN at all.
//
// Stripping the query string and the "file:" scheme is what turns a usable DSN
// into a path for the lock and for the job logs. The in-memory spellings are
// checked before the stripping, because "mode=memory" only means memory while
// it is still part of the DSN.
func databaseFilePath(dsn string) string {
	if dsn == "" || strings.HasPrefix(dsn, ":memory:") || strings.Contains(dsn, "mode=memory") {
		return ""
	}
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		dsn = dsn[:i]
	}
	return strings.TrimPrefix(dsn, "file:")
}
