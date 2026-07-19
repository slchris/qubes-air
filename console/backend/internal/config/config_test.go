package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.False(t, cfg.Server.TLS.Enabled)
	assert.Equal(t, "./qubes-air.db", cfg.Database.DSN)
	assert.Contains(t, cfg.CORS.AllowedOrigins, "*")
}

func TestConfig_Address(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, "0.0.0.0:8080", cfg.Address())

	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 9090
	assert.Equal(t, "127.0.0.1:9090", cfg.Address())
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
	}{
		{
			name:    "default config is valid",
			modify:  func(c *Config) {},
			wantErr: false,
		},
		{
			name: "invalid port zero",
			modify: func(c *Config) {
				c.Server.Port = 0
			},
			wantErr: true,
		},
		{
			name: "invalid port too high",
			modify: func(c *Config) {
				c.Server.Port = 70000
			},
			wantErr: true,
		},
		{
			name: "TLS enabled without cert",
			modify: func(c *Config) {
				c.Server.TLS.Enabled = true
				c.Server.TLS.KeyFile = "/tmp/key.pem"
			},
			wantErr: true,
		},
		{
			name: "TLS enabled without key",
			modify: func(c *Config) {
				c.Server.TLS.Enabled = true
				c.Server.TLS.CertFile = "/tmp/cert.pem"
			},
			wantErr: true,
		},
		{
			name: "orchestrator enabled without terraform dir",
			modify: func(c *Config) {
				c.Orchestrator.Enabled = true
				c.Orchestrator.TerraformDir = ""
			},
			wantErr: true,
		},
		{
			name: "orchestrator enabled with terraform dir",
			modify: func(c *Config) {
				c.Orchestrator.Enabled = true
				c.Orchestrator.TerraformDir = "/tf"
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			err := cfg.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfig_LoadFromEnv(t *testing.T) {
	originalHost := os.Getenv("QUBES_AIR_HOST")
	originalPort := os.Getenv("QUBES_AIR_PORT")
	originalDSN := os.Getenv("QUBES_AIR_DATABASE_DSN")
	originalOrigins := os.Getenv("QUBES_AIR_CORS_ORIGINS")

	defer func() {
		os.Setenv("QUBES_AIR_HOST", originalHost)
		os.Setenv("QUBES_AIR_PORT", originalPort)
		os.Setenv("QUBES_AIR_DATABASE_DSN", originalDSN)
		os.Setenv("QUBES_AIR_CORS_ORIGINS", originalOrigins)
	}()

	os.Setenv("QUBES_AIR_HOST", "192.168.1.1")
	os.Setenv("QUBES_AIR_PORT", "9999")
	os.Setenv("QUBES_AIR_DATABASE_DSN", "/data/test.db")
	os.Setenv("QUBES_AIR_CORS_ORIGINS", "http://localhost:3000,http://localhost:5173")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "192.168.1.1", cfg.Server.Host)
	assert.Equal(t, 9999, cfg.Server.Port)
	assert.Equal(t, "/data/test.db", cfg.Database.DSN)
	assert.Contains(t, cfg.CORS.AllowedOrigins, "http://localhost:3000")
	assert.Contains(t, cfg.CORS.AllowedOrigins, "http://localhost:5173")
}

func TestConfig_LoadFromFile(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	configContent := `
server:
  host: "10.0.0.1"
  port: 8888
  mode: "debug"
  tls:
    enabled: false

database:
  dsn: "/var/lib/qubes-air/data.db"

cors:
  allowed_origins:
    - "https://example.com"
    - "https://app.example.com"
`
	_, err = tmpFile.WriteString(configContent)
	require.NoError(t, err)
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	require.NoError(t, err)

	assert.Equal(t, "10.0.0.1", cfg.Server.Host)
	assert.Equal(t, 8888, cfg.Server.Port)
	assert.Equal(t, "debug", cfg.Server.Mode)
	assert.Equal(t, "/var/lib/qubes-air/data.db", cfg.Database.DSN)
	assert.Contains(t, cfg.CORS.AllowedOrigins, "https://example.com")
}

func TestConfig_IsTLSEnabled(t *testing.T) {
	cfg := DefaultConfig()
	assert.False(t, cfg.IsTLSEnabled())

	cfg.Server.TLS.Enabled = true
	assert.True(t, cfg.IsTLSEnabled())
}

func TestConfig_EncryptionKeyBytes(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
		wantDev bool
	}{
		{"empty falls back to dev key", "", false, true},
		{"valid 32-byte key", "0123456789abcdef0123456789abcdef", false, false},
		{"too short", "short", true, false},
		{"too long", "0123456789abcdef0123456789abcdef0", true, false},
		{"the dev key itself is accepted", devEncryptionKey, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Security.EncryptionKey = tt.key

			b, err := cfg.EncryptionKeyBytes()
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, b, 32)
			assert.Equal(t, tt.wantDev, cfg.UsesDevEncryptionKey())
		})
	}
}

func TestConfig_ValidateRejectsBadEncryptionKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Security.EncryptionKey = "not-32-bytes"
	assert.Error(t, cfg.Validate())

	cfg.Security.EncryptionKey = "0123456789abcdef0123456789abcdef"
	assert.NoError(t, cfg.Validate())
}

func TestConfig_Keyring_SingleKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Security.EncryptionKey = "0123456789abcdef0123456789abcdef"

	kr, err := cfg.Keyring()
	require.NoError(t, err)
	assert.Equal(t, 1, kr.PrimaryVersion())
	assert.False(t, cfg.UsesDevEncryptionKey())
}

func TestConfig_Keyring_DevFallback(t *testing.T) {
	cfg := DefaultConfig() // no key set
	kr, err := cfg.Keyring()
	require.NoError(t, err)
	assert.Equal(t, 1, kr.PrimaryVersion())
	assert.True(t, cfg.UsesDevEncryptionKey())
}

func TestConfig_Keyring_MultiVersion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Security.EncryptionKeys = "v1:0123456789abcdef0123456789abcdef,v2:fedcba9876543210fedcba9876543210"

	kr, err := cfg.Keyring()
	require.NoError(t, err)
	assert.Equal(t, 2, kr.PrimaryVersion(), "highest version is primary")
	assert.Equal(t, []int{1, 2}, kr.Versions())
	// Multi-version spec is never the dev key.
	assert.False(t, cfg.UsesDevEncryptionKey())
}

func TestConfig_Validate_RejectsBadKeyring(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Security.EncryptionKeys = "v1:short"
	assert.Error(t, cfg.Validate())
}

func TestConfig_LoadFromEnv_MultiVersionKeys(t *testing.T) {
	t.Setenv("QUBES_AIR_ENCRYPTION_KEYS",
		"v1:0123456789abcdef0123456789abcdef,v2:fedcba9876543210fedcba9876543210")

	cfg, err := Load("")
	require.NoError(t, err)
	kr, err := cfg.Keyring()
	require.NoError(t, err)
	assert.Equal(t, 2, kr.PrimaryVersion())
}

func TestConfig_IsAuthEnabled(t *testing.T) {
	cfg := DefaultConfig()
	assert.False(t, cfg.IsAuthEnabled())

	cfg.Auth.APIToken = "token"
	assert.True(t, cfg.IsAuthEnabled())
}

func TestConfig_LoadFromEnvSecurity(t *testing.T) {
	t.Setenv("QUBES_AIR_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("QUBES_AIR_API_TOKEN", "env-token")

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef", cfg.Security.EncryptionKey)
	assert.Equal(t, "env-token", cfg.Auth.APIToken)
	assert.True(t, cfg.IsAuthEnabled())
	assert.False(t, cfg.UsesDevEncryptionKey())
}

// TestConfig_ValidateAgentPackage — the agent .deb is fetched at boot from an
// artifact store with no authentication, served over plain HTTP. The SHA256 is
// the only integrity control in that chain, so the half-configured cases are
// rejected at startup: caught here it is a failed boot of the console, caught
// later it is a fleet of qubes with no agent and nobody watching.
func TestConfig_ValidateAgentPackage(t *testing.T) {
	const goodURL = "http://10.31.0.2/local/qubes-air/qubes-air-agent_0.1.0_amd64.deb"
	const goodSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	tests := []struct {
		name    string
		url     string
		sha     string
		wantErr bool
	}{
		{name: "unset is valid: delivery is opt-in", wantErr: false},
		{name: "url and digest together", url: goodURL, sha: goodSHA, wantErr: false},
		{name: "uppercase digest is accepted", url: goodURL, sha: strings.ToUpper(goodSHA), wantErr: false},
		// A URL without a digest would install whatever the store was serving,
		// as root, on every new qube.
		{name: "url without digest is refused", url: goodURL, wantErr: true},
		{name: "digest without url", sha: goodSHA, wantErr: true},
		// A truncated or mistyped digest fails verification on a perfectly good
		// package, and the only symptom is a qube that comes up with no agent.
		{name: "short digest", url: goodURL, sha: "abc123", wantErr: true},
		{name: "non-hex digest", url: goodURL, sha: strings.Repeat("z", 64), wantErr: true},
		{name: "non-http url", url: "file:///tmp/agent.deb", sha: goodSHA, wantErr: true},
		// The URL is interpolated into a root shell script in every guest.
		{name: "quote in url", url: goodURL + "';reboot;'", sha: goodSHA, wantErr: true},
		{name: "space in url", url: "http://10.31.0.2/a b.deb", sha: goodSHA, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Orchestrator.AgentPackageURL = tt.url
			cfg.Orchestrator.AgentPackageSHA256 = tt.sha

			err := cfg.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestConfig_LoadFromEnvAgentPackage — deployment sets these from the
// environment; a binding that silently does not read is a console that renders
// unpinned identity documents while its config file says otherwise.
func TestConfig_LoadFromEnvAgentPackage(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("QUBES_AIR_AGENT_PACKAGE_URL", "http://10.31.0.2/local/qubes-air/qubes-air-agent_0.1.0_amd64.deb")
	t.Setenv("QUBES_AIR_AGENT_PACKAGE_SHA256", sha)
	t.Setenv("QUBES_AIR_AGENT_PACKAGE_VERSION", "0.1.0")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, "http://10.31.0.2/local/qubes-air/qubes-air-agent_0.1.0_amd64.deb", cfg.Orchestrator.AgentPackageURL)
	assert.Equal(t, sha, cfg.Orchestrator.AgentPackageSHA256)
	assert.Equal(t, "0.1.0", cfg.Orchestrator.AgentPackageVersion)
}

// TestConfig_AgentProbeDefaults — probing must be on out of the box. A console
// that only reports agent health when someone remembered to configure it is a
// console that reports nothing on the day an agent dies.
func TestConfig_AgentProbeDefaults(t *testing.T) {
	cfg := DefaultConfig()

	assert.Positive(t, cfg.Orchestrator.AgentProbeIntervalSeconds,
		"a zero interval disables the reconciler, so a dead agent would never be noticed")
	assert.Positive(t, cfg.Orchestrator.AgentProbeTimeoutSeconds)
	assert.Positive(t, cfg.Orchestrator.AgentProbeSettleSeconds)
	assert.Less(t, cfg.Orchestrator.AgentProbeTimeoutSeconds, cfg.Orchestrator.AgentProbeIntervalSeconds,
		"a probe that can outlast its own interval would have sweeps overlapping forever")
}

// TestConfig_LoadFromEnvAgentProbe — the probe timings are what an operator
// reaches for when a fleet is too large for a 60s sweep. A binding that does not
// read leaves them tuning a file the process ignores.
func TestConfig_LoadFromEnvAgentProbe(t *testing.T) {
	t.Setenv("QUBES_AIR_AGENT_PROBE_INTERVAL_SECONDS", "300")
	t.Setenv("QUBES_AIR_AGENT_PROBE_TIMEOUT_SECONDS", "20")
	t.Setenv("QUBES_AIR_AGENT_PROBE_SETTLE_SECONDS", "900")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, 300, cfg.Orchestrator.AgentProbeIntervalSeconds)
	assert.Equal(t, 20, cfg.Orchestrator.AgentProbeTimeoutSeconds)
	assert.Equal(t, 900, cfg.Orchestrator.AgentProbeSettleSeconds)
}

// TestConfig_AgentProbeEnvGarbageKeepsTheDefault — an unparseable value must not
// resolve to zero. For the interval that would silently switch periodic probing
// off, which looks exactly like a fleet where nothing has ever gone wrong.
func TestConfig_AgentProbeEnvGarbageKeepsTheDefault(t *testing.T) {
	t.Setenv("QUBES_AIR_AGENT_PROBE_INTERVAL_SECONDS", "sixty")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, DefaultConfig().Orchestrator.AgentProbeIntervalSeconds,
		cfg.Orchestrator.AgentProbeIntervalSeconds)
}
