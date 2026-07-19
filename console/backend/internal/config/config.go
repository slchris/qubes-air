// Package config provides application configuration management.
package config

import (
	"fmt"
	"os"
	"path/filepath"
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
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Host string    `yaml:"host"`
	Port int       `yaml:"port"`
	Mode string    `yaml:"mode"`
	TLS  TLSConfig `yaml:"tls"`
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
//     version 1, preserving pre-rotation behaviour and legacy rows.
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

// Address returns the server listen address.
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

// IsTLSEnabled returns whether TLS is enabled.
func (c *Config) IsTLSEnabled() bool {
	return c.Server.TLS.Enabled
}
