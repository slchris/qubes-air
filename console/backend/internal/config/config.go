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
// executor: it flips the DB status without invoking terraform. This keeps the
// console runnable on machines without a cloud/terraform environment.
//
// When Enabled is true, start/stop shell out to terraform in TerraformDir to
// perform compute/storage separation (suspend/resume).
type OrchestratorConfig struct {
	// Enabled turns on the real TerraformExecutor. Env: QUBES_AIR_ORCHESTRATOR_ENABLED.
	Enabled bool `yaml:"enabled"`
	// TerraformDir is the terraform root directory to run in (required when
	// Enabled). Env: QUBES_AIR_TERRAFORM_DIR.
	TerraformDir string `yaml:"terraform_dir"`
	// TerraformBinary overrides the terraform executable (default "terraform").
	// Env: QUBES_AIR_TERRAFORM_BINARY.
	TerraformBinary string `yaml:"terraform_binary"`
	// VarFile is the OPERATOR-owned base -var-file (endpoint, node, zone
	// toggles). It must NOT define remote_qubes — see GeneratedVarFile.
	// Env: QUBES_AIR_TERRAFORM_VAR_FILE.
	VarFile string `yaml:"var_file"`
	// GeneratedVarFile is the CONSOLE-owned var-file holding the remote_qubes
	// map rendered from the database. Relative paths resolve against
	// TerraformDir. It is always passed to terraform AFTER VarFile, because
	// terraform lets the last -var-file win for a given variable — that
	// ordering is what stops a hand-edited tfvars from silently overriding the
	// console's view of which qubes exist.
	// Env: QUBES_AIR_TERRAFORM_GENERATED_VAR_FILE.
	GeneratedVarFile string `yaml:"generated_var_file"`
	// AgentIdentityDir holds the rendered cloud-init documents that deliver
	// each agent's mTLS identity. They contain PRIVATE KEYS, so the directory
	// is created 0700 and the files 0600.
	//
	// Terraform is given the PATH of a file, never its content: source_file
	// records only the path and volume id in state, while inlining the content
	// would put a private key into state in plaintext.
	// Env: QUBES_AIR_AGENT_IDENTITY_DIR.
	AgentIdentityDir string `yaml:"agent_identity_dir"`
	// AgentListen is the address the remote agent binds on (default 0.0.0.0:8443).
	// Env: QUBES_AIR_AGENT_LISTEN.
	AgentListen string `yaml:"agent_listen"`
	// ProxmoxSSHKeyFile is the private key the terraform provider uses to SSH
	// into PVE nodes, and ProxmoxSSHUsername the login (default "root").
	//
	// Required for provisioning, not optional: uploading a cloud-init snippet
	// writes /var/lib/vz/snippets/ on the node over SSH and the PVE API has no
	// endpoint for it. That snippet carries the per-qube agent identity, so
	// without this every provision fails partway — after the VM has been
	// cloned, which leaves a half-built qube behind.
	//
	// A PATH, not the key itself: the content is read at call time and passed
	// to terraform as a TF_VAR_, so it never lands in the terraform root or in
	// state. Keeping the path in config means the unit file and any process
	// listing show a filename rather than a private key.
	// Env: QUBES_AIR_PROXMOX_SSH_KEY_FILE / QUBES_AIR_PROXMOX_SSH_USERNAME.
	ProxmoxSSHKeyFile  string `yaml:"proxmox_ssh_key_file"`
	ProxmoxSSHUsername string `yaml:"proxmox_ssh_username"`
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
	// (console -> terraform SFTP -> Proxmox snippet -> cloud-init).
	// Env: QUBES_AIR_AGENT_PACKAGE_SHA256.
	AgentPackageSHA256 string `yaml:"agent_package_sha256"`
	// AgentPackageVersion is the version this console expects to deliver.
	// Advisory only — the digest is what is enforced — but it is what makes a
	// guest log line and an audit trail name a specific build.
	// Env: QUBES_AIR_AGENT_PACKAGE_VERSION.
	AgentPackageVersion string `yaml:"agent_package_version"`
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
	// A newly built qube CANNOT answer when terraform returns: cloud-init only
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
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Host string    `yaml:"host"`
	Port int       `yaml:"port"`
	Mode string    `yaml:"mode"`
	TLS  TLSConfig `yaml:"tls"`

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
	APIToken string `yaml:"api_token"`
}

// devEncryptionKey is the well-known insecure key used only when no key is
// configured, to keep local development and tests frictionless.
const devEncryptionKey = "qubes-air-dev-encryption-key32!!" // 32 bytes for AES-256

// IsAuthEnabled reports whether API authentication is enforced.
func (c *Config) IsAuthEnabled() bool {
	return c.Auth.APIToken != ""
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
			Host: "0.0.0.0",
			Port: 8080,
			Mode: gin.ReleaseMode,
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
			// executor). Enable and set terraform_dir to drive real suspend/
			// resume.
			Enabled:         false,
			TerraformBinary: "terraform",
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
		},
		Transport: TransportConfig{
			// Disabled by default: no gRPC transport wired (noop). Enable and
			// set remote_endpoint + mTLS files to drive cross-machine qrexec.
			Enabled:             false,
			KeepAliveSeconds:    20,
			ReconnectMinSeconds: 1,
			ReconnectMaxSeconds: 30,
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

// loadFromEnv loads configuration from environment variables.
// One independent lookup per configuration field. The complexity score counts
// fields, not difficulty: there are no interacting branches here, and cutting
// it into loadNetworkEnv/loadTLSEnv/... would only move the same flat list.
//
//nolint:gocyclo,funlen // flat per-field sequence; both scores track field count
func (c *Config) loadFromEnv() {
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

	if dsn := os.Getenv("QUBES_AIR_DATABASE_DSN"); dsn != "" {
		c.Database.DSN = dsn
	}

	if origins := os.Getenv("QUBES_AIR_CORS_ORIGINS"); origins != "" {
		c.CORS.AllowedOrigins = strings.Split(origins, ",")
	}

	if key := os.Getenv("QUBES_AIR_ENCRYPTION_KEY"); key != "" {
		c.Security.EncryptionKey = key
	}
	if keys := os.Getenv("QUBES_AIR_ENCRYPTION_KEYS"); keys != "" {
		c.Security.EncryptionKeys = keys
	}
	if token := os.Getenv("QUBES_AIR_API_TOKEN"); token != "" {
		c.Auth.APIToken = token
	}

	if enabled := os.Getenv("QUBES_AIR_ORCHESTRATOR_ENABLED"); enabled != "" {
		c.Orchestrator.Enabled = strings.ToLower(enabled) == "true"
	}
	if dir := os.Getenv("QUBES_AIR_TERRAFORM_DIR"); dir != "" {
		c.Orchestrator.TerraformDir = dir
	}
	if bin := os.Getenv("QUBES_AIR_TERRAFORM_BINARY"); bin != "" {
		c.Orchestrator.TerraformBinary = bin
	}
	if varFile := os.Getenv("QUBES_AIR_TERRAFORM_VAR_FILE"); varFile != "" {
		c.Orchestrator.VarFile = varFile
	}
	if genVarFile := os.Getenv("QUBES_AIR_TERRAFORM_GENERATED_VAR_FILE"); genVarFile != "" {
		c.Orchestrator.GeneratedVarFile = genVarFile
	}
	if dir := os.Getenv("QUBES_AIR_AGENT_IDENTITY_DIR"); dir != "" {
		c.Orchestrator.AgentIdentityDir = dir
	}
	if listen := os.Getenv("QUBES_AIR_AGENT_LISTEN"); listen != "" {
		c.Orchestrator.AgentListen = listen
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
	if v := os.Getenv("QUBES_AIR_APT_MIRROR"); v != "" {
		c.Orchestrator.AptMirror = v
	}
	if v := os.Getenv("QUBES_AIR_APT_SECURITY_MIRROR"); v != "" {
		c.Orchestrator.AptSecurityMirror = v
	}
	if url := os.Getenv("QUBES_AIR_AGENT_PACKAGE_URL"); url != "" {
		c.Orchestrator.AgentPackageURL = url
	}
	if sha := os.Getenv("QUBES_AIR_AGENT_PACKAGE_SHA256"); sha != "" {
		c.Orchestrator.AgentPackageSHA256 = sha
	}
	if v := os.Getenv("QUBES_AIR_AGENT_PACKAGE_VERSION"); v != "" {
		c.Orchestrator.AgentPackageVersion = v
	}
	// Parsed with Atoi and applied only on success, matching the transport
	// timings below. A typo therefore keeps the default rather than silently
	// resolving to 0, which for the interval would disable probing outright.
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
	if v := os.Getenv("QUBES_AIR_TRANSPORT_REVERSE_LOCAL_TARGET"); v != "" {
		c.Transport.ReverseLocalTarget = v
	}
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

// Validate checks if the configuration is valid.
// Same shape as loadFromEnv: one check per field, no interaction between them.
//
//nolint:gocyclo // flat per-field sequence; the score tracks field count
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Server.Port)
	}

	if c.Server.TLS.Enabled {
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
	}

	// Fail fast on a misconfigured encryption key rather than silently
	// falling back to the insecure default. Building the keyring validates both
	// the single-key and multi-version forms (length, version syntax, primary).
	if _, err := c.Keyring(); err != nil {
		return err
	}

	// If real orchestration is enabled, a terraform working directory is
	// mandatory — otherwise start/stop would fail at runtime.
	if c.Orchestrator.Enabled && c.Orchestrator.TerraformDir == "" {
		return fmt.Errorf("orchestrator.enabled is true but orchestrator.terraform_dir is not set")
	}

	// A package URL without a digest would have every new qube install, as root,
	// whatever the unauthenticated artifact store happened to be serving.
	// Refusing at startup is the loud place to catch it: the renderer's own
	// fallback is a qube that comes up with no agent, which is only discovered
	// when something tries to reach it.
	if err := validateAgentPackage(c.Orchestrator.AgentPackageURL, c.Orchestrator.AgentPackageSHA256); err != nil {
		return err
	}

	// If the gRPC transport is enabled, the remote endpoint and mTLS material
	// are mandatory — otherwise the outbound tunnel would fail at runtime.
	if c.Transport.Enabled {
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

// JobLogDir is where terraform output is kept, one file per job.
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
	dsn := c.Database.DSN
	if dsn == "" || strings.HasPrefix(dsn, ":memory:") || strings.Contains(dsn, "mode=memory") {
		return ""
	}
	// Strip any sqlite query string ("file.db?_busy_timeout=...") before
	// treating it as a path.
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		dsn = dsn[:i]
	}
	dsn = strings.TrimPrefix(dsn, "file:")
	if dsn == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dsn), "job-logs")
}
