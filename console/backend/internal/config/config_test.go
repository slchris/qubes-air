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
			name: "production without an API token fails closed",
			modify: func(c *Config) {
				c.Server.Production = true
				c.Auth.APIToken = ""
				c.Auth.Tokens = nil
			},
			wantErr: true,
		},
		{
			name: "production with the development encryption key fails closed",
			modify: func(c *Config) {
				c.Server.Production = true
				c.Auth.APIToken = "prod-token"
				c.Security.EncryptionKey = ""
			},
			wantErr: true,
		},
		{
			name: "production with wildcard CORS fails closed",
			modify: func(c *Config) {
				c.Server.Production = true
				c.Auth.APIToken = "prod-token"
				c.Security.EncryptionKey = "0123456789abcdef0123456789abcdef"
				c.CORS.AllowedOrigins = []string{"*"}
			},
			wantErr: true,
		},
		{
			name: "production with token, real key and restricted CORS is valid",
			modify: func(c *Config) {
				c.Server.Production = true
				c.Auth.APIToken = "prod-token"
				c.Security.EncryptionKey = "0123456789abcdef0123456789abcdef"
				c.CORS.AllowedOrigins = []string{"https://console.local"}
			},
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
			name: "orchestrator enabled uses native executor",
			modify: func(c *Config) {
				c.Orchestrator.Enabled = true
				c.Orchestrator.AgentRevocationURL = "https://console.example/pki/revocations"
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

// TestConfig_IsAuthEnabled_ScopedTokens — the scoped list enables enforcement
// exactly like the legacy administrator token; an entry with an empty token
// (which Validate would refuse at startup) does not count.
func TestConfig_IsAuthEnabled_ScopedTokens(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.Tokens = []ScopedToken{{Name: "read", Token: "rt", Scope: "read-only"}}
	assert.True(t, cfg.IsAuthEnabled())

	cfg.Auth.Tokens = []ScopedToken{{Name: "empty", Token: "", Scope: "control"}}
	assert.False(t, cfg.IsAuthEnabled())
}

// TestConfig_ValidateAuth — a malformed token list must fail Validate: an empty
// token or an unknown scope would otherwise be silently skipped, denying an
// operator the protection they believe they configured.
func TestConfig_ValidateAuth(t *testing.T) {
	tests := []struct {
		name    string
		tokens  []ScopedToken
		wantErr string
	}{
		{
			name:   "no tokens is valid (auth disabled)",
			tokens: nil,
		},
		{
			name:   "read-only and control are valid",
			tokens: []ScopedToken{{Name: "r", Token: "rt", Scope: "read-only"}, {Name: "c", Token: "ct", Scope: "control"}},
		},
		{
			name:    "empty token is refused",
			tokens:  []ScopedToken{{Name: "r", Token: "", Scope: "read-only"}},
			wantErr: "must not be empty",
		},
		{
			name:    "unknown scope is refused",
			tokens:  []ScopedToken{{Name: "r", Token: "rt", Scope: "admin"}},
			wantErr: `scope must be "read-only" or "control"`,
		},
		{
			name:    "empty scope is refused",
			tokens:  []ScopedToken{{Name: "r", Token: "rt", Scope: ""}},
			wantErr: `scope must be "read-only" or "control"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Auth.Tokens = tt.tokens
			err := cfg.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestConfig_LoadFromFileScopedTokens — the token list is a config-file
// surface; a binding that silently does not read leaves the console open while
// the file promises tokens.
func TestConfig_LoadFromFileScopedTokens(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config-tokens-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	content := `
auth:
  api_token: ""
  tokens:
    - name: "read"
      token: "reader-secret"
      scope: "read-only"
    - name: "ops"
      token: "ops-secret"
      scope: "control"
`
	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	require.NoError(t, err)
	require.Len(t, cfg.Auth.Tokens, 2)
	assert.Equal(t, ScopedToken{Name: "read", Token: "reader-secret", Scope: "read-only"}, cfg.Auth.Tokens[0])
	assert.Equal(t, ScopedToken{Name: "ops", Token: "ops-secret", Scope: "control"}, cfg.Auth.Tokens[1])
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

// TestConfig_JobTimeoutOutlivesAProvision — the default bound has to sit above
// the documented 15-25 minute provision, not inside it. A bound below the work
// it wraps cancels a healthy job after its VM and disk already exist.
func TestConfig_JobTimeoutOutlivesAProvision(t *testing.T) {
	assert.GreaterOrEqual(t, DefaultConfig().Orchestrator.JobTimeoutSeconds, 30*60,
		"the default job timeout must clear the documented 15-25 minute provision")

	t.Setenv("QUBES_AIR_ORCHESTRATOR_JOB_TIMEOUT_SECONDS", "120")
	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, 120, cfg.Orchestrator.JobTimeoutSeconds)

	t.Setenv("QUBES_AIR_ORCHESTRATOR_JOB_TIMEOUT_SECONDS", "forever")
	cfg, err = Load("")
	require.NoError(t, err)
	assert.Equal(t, DefaultConfig().Orchestrator.JobTimeoutSeconds,
		cfg.Orchestrator.JobTimeoutSeconds, "an unparseable value keeps the default")
}

func TestOrchestratorRequiresRevocationURL(t *testing.T) {
	c := DefaultConfig()
	c.Orchestrator.Enabled = true
	assert.Error(t, c.Validate())
}

// TestConfig_ValidateTokenZones — a zone allowlist must be an exact list of
// zone IDs; a wildcard or a sloppy entry is refused so a misconfigured token
// cannot silently match nothing (or everything).
func TestConfig_ValidateTokenZones(t *testing.T) {
	valid := func(zones []string) *Config {
		cfg := DefaultConfig()
		cfg.Auth.Tokens = []ScopedToken{{Name: "t", Token: "v", Scope: "read-only", Zones: zones}}
		return cfg
	}
	require.NoError(t, valid(nil).Validate())
	require.NoError(t, valid([]string{"z1", "z2"}).Validate())

	for name, zones := range map[string][]string{
		"empty entry":  {""},
		"whitespace":   {" z1"},
		"wildcard":     {"*"},
		"duplicate":    {"z1", "z1"},
		"empty middle": {"z1", ""},
	} {
		err := valid(zones).Validate()
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "zones", name)
	}
}

// The allowlists are validated at startup so a typo fails here instead of
// surfacing as a refused call inside a guest nobody is watching.
func TestConfig_ValidateRejectsUnsafePathAllowlists(t *testing.T) {
	cases := []struct {
		name    string
		modify  func(cfg *Config)
		wantErr bool
	}{
		{name: "default is empty and valid", modify: func(*Config) {}},
		{name: "absolute exec", modify: func(c *Config) { c.Orchestrator.AgentExecAllow = []string{"/usr/bin/id"} }, wantErr: false},
		{name: "absolute filecopy root", modify: func(c *Config) { c.Orchestrator.AgentFileCopyRoots = []string{"/var/tmp"} }, wantErr: false},
		{name: "relative exec", modify: func(c *Config) { c.Orchestrator.AgentExecAllow = []string{"usr/bin/id"} }, wantErr: true},
		{name: "exec with dotdot", modify: func(c *Config) { c.Orchestrator.AgentExecAllow = []string{"/usr/bin/../sbin/x"} }, wantErr: true},
		{name: "exec with colon", modify: func(c *Config) { c.Orchestrator.AgentExecAllow = []string{"/a:/b"} }, wantErr: true},
		{name: "exec with newline", modify: func(c *Config) {
			c.Orchestrator.AgentExecAllow = []string{"/usr/bin/id\nQUBESAIR_ALLOW=everything"}
		}, wantErr: true},
		{name: "filecopy root is slash", modify: func(c *Config) { c.Orchestrator.AgentFileCopyRoots = []string{"/"} }, wantErr: true},
		{name: "filecopy root relative", modify: func(c *Config) { c.Orchestrator.AgentFileCopyRoots = []string{"var/tmp"} }, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.modify(cfg)
			err := cfg.Validate()
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// The env vars use the agent's own separator (colon) so a value copied out of a
// guest's agent.env means the same thing on the console. An empty entry is kept
// rather than dropped: "a::b" is a typo the validator must reject, not silently
// repair into a list the operator did not write.
func TestConfig_LoadFromEnv_SplitsPathAllowlistsOnColon(t *testing.T) {
	t.Setenv("QUBES_AIR_EXEC_ALLOW", "/usr/bin/id:/usr/bin/uptime")
	t.Setenv("QUBES_AIR_FILECOPY_ROOTS", "/var/tmp:/srv/in")

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/bin/id", "/usr/bin/uptime"}, cfg.Orchestrator.AgentExecAllow)
	assert.Equal(t, []string{"/var/tmp", "/srv/in"}, cfg.Orchestrator.AgentFileCopyRoots)

	// "a::b" survives parsing (splitColon keeps the empty entry) and is then
	// refused by validation, which Load runs: the typo never reaches a provision.
	t.Setenv("QUBES_AIR_EXEC_ALLOW", "/usr/bin/id::/bin/sh")
	_, err = Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty entry")
}

// TestConfig_QubeSpecBoundsDefaults — the shipped bounds must be enforceable:
// a minimum below 1 bounds nothing and a maximum below its minimum refuses every
// request, and neither shows up until someone tries to create a qube.
func TestConfig_QubeSpecBoundsDefaults(t *testing.T) {
	q := DefaultConfig().QubeSpec

	require.NoError(t, q.Validate(), "the shipped bounds must pass their own validation")
	require.NoError(t, DefaultConfig().Validate())

	for _, d := range q.dimensions() {
		assert.GreaterOrEqualf(t, d.min, 1, "qube_spec.min_%s must bound something", d.key)
		assert.GreaterOrEqualf(t, d.max, d.min, "qube_spec.max_%s must not be below its minimum", d.key)
	}

	// The three numbers the UI already constrains must stay in step with it
	// (QubeList.svelte's own min/max), or the API would refuse what the form
	// offers, or accept what it blocks.
	assert.Equal(t, 1, q.MinVCPU)
	assert.Equal(t, 32, q.MaxVCPU, "the create/edit form's max=\"32\" must be the API's maximum")
	assert.Equal(t, 512, q.MinMemoryMB, "the form's min=\"512\" must be the API's minimum")
	assert.Equal(t, 10, q.MinDiskGB, "the form's min=\"10\" for the root disk")
	assert.Equal(t, 1, q.MinDataDiskGB, "the form's min=\"1\" for the data disk")
}

// TestConfig_QubeSpecBoundsFromEnv — a deployment sets these from its unit file
// or environment. A binding that silently does not read leaves the operator
// tuning a value the process ignores, which looks exactly like a bound that is
// not enforced.
func TestConfig_QubeSpecBoundsFromEnv(t *testing.T) {
	for name, value := range map[string]string{
		"QUBES_AIR_QUBE_SPEC_MIN_VCPU":         "2",
		"QUBES_AIR_QUBE_SPEC_MAX_VCPU":         "64",
		"QUBES_AIR_QUBE_SPEC_MIN_MEMORY_MB":    "1024",
		"QUBES_AIR_QUBE_SPEC_MAX_MEMORY_MB":    "524288",
		"QUBES_AIR_QUBE_SPEC_MIN_DISK_GB":      "20",
		"QUBES_AIR_QUBE_SPEC_MAX_DISK_GB":      "32768",
		"QUBES_AIR_QUBE_SPEC_MIN_DATA_DISK_GB": "5",
		"QUBES_AIR_QUBE_SPEC_MAX_DATA_DISK_GB": "32768",
		"QUBES_AIR_QUBE_SPEC_MIN_GPU_COUNT":    "2",
		"QUBES_AIR_QUBE_SPEC_MAX_GPU_COUNT":    "16",
	} {
		t.Setenv(name, value)
	}

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, QubeSpecConfig{
		MinVCPU: 2, MaxVCPU: 64,
		MinMemoryMB: 1024, MaxMemoryMB: 524288,
		MinDiskGB: 20, MaxDiskGB: 32768,
		MinDataDiskGB: 5, MaxDataDiskGB: 32768,
		MinGPUCount: 2, MaxGPUCount: 16,
	}, cfg.QubeSpec)
}

// TestConfig_QubeSpecBoundsEnvGarbageKeepsTheDefault — an unparseable value must
// not resolve to 0. For a minimum that would drop the lower bound, and for a
// maximum it would refuse every create, so a typo has to keep the default.
func TestConfig_QubeSpecBoundsEnvGarbageKeepsTheDefault(t *testing.T) {
	t.Setenv("QUBES_AIR_QUBE_SPEC_MIN_DISK_GB", "ten")
	t.Setenv("QUBES_AIR_QUBE_SPEC_MAX_DISK_GB", "lots")

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, DefaultConfig().QubeSpec.MinDiskGB, cfg.QubeSpec.MinDiskGB)
	assert.Equal(t, DefaultConfig().QubeSpec.MaxDiskGB, cfg.QubeSpec.MaxDiskGB)
}

// TestConfig_QubeSpecBoundsValidation — a bounds set that cannot be enforced is
// refused at startup rather than applied. Both shapes are one transposed digit
// away from a correct one, and both are invisible until a create is attempted.
func TestConfig_QubeSpecBoundsValidation(t *testing.T) {
	for name, modify := range map[string]func(*QubeSpecConfig){
		"min above max":     func(q *QubeSpecConfig) { q.MinVCPU, q.MaxVCPU = 64, 32 },
		"min zero":          func(q *QubeSpecConfig) { q.MinMemoryMB = 0 },
		"min negative":      func(q *QubeSpecConfig) { q.MinDiskGB = -1 },
		"max zero":          func(q *QubeSpecConfig) { q.MaxDataDiskGB = 0 },
		"gpu min below one": func(q *QubeSpecConfig) { q.MinGPUCount = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			modify(&cfg.QubeSpec)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "qube_spec.", "the message must name the key to fix")
		})
	}

	// A bound that is merely different from the default is fine — the keys exist
	// so a smaller cluster can lower the ceiling.
	cfg := DefaultConfig()
	cfg.QubeSpec.MaxVCPU = 8
	assert.NoError(t, cfg.Validate())

	// And it is refused on the LOAD path too, so a bad env var cannot start a
	// console whose bound refuses everything.
	t.Setenv("QUBES_AIR_QUBE_SPEC_MIN_VCPU", "64")
	t.Setenv("QUBES_AIR_QUBE_SPEC_MAX_VCPU", "32")
	_, err := Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qube_spec.max_vcpu")
}

// TestLockFilePathDerivesFromTheDatabase pins the default that makes the
// single-instance lock meaningful: the lock file is the database file's own
// path plus ".lock", so two consoles sharing a database necessarily contend for
// one lock, while two consoles with different databases in one directory do not.
func TestLockFilePathDerivesFromTheDatabase(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, "./qubes-air.db.lock", cfg.LockFilePath(),
		"the default database must derive a lock beside it")

	// Different databases in the same directory must NOT share a lock: a fixed
	// name such as <dir>/qubes-air.lock would refuse a legitimate pairing.
	cfg.Database.DSN = "/var/lib/qubes-air/one.db"
	one := cfg.LockFilePath()
	cfg.Database.DSN = "/var/lib/qubes-air/two.db"
	two := cfg.LockFilePath()
	assert.Equal(t, "/var/lib/qubes-air/one.db.lock", one)
	assert.NotEqual(t, one, two, "different databases must not contend for one lock")

	// A DSN carrying sqlite options, or the file: scheme, still names its file.
	cfg.Database.DSN = "file:/var/lib/qubes-air/three.db?_busy_timeout=5000"
	assert.Equal(t, "/var/lib/qubes-air/three.db.lock", cfg.LockFilePath())

	// An explicit lock_file wins. It is also the only way to lock an in-memory
	// database, which has no path to derive one from.
	cfg.LockFile = "/run/qubes-air/console.lock"
	assert.Equal(t, "/run/qubes-air/console.lock", cfg.LockFilePath())

	// In-memory: no file another process could share, so nothing to exclude.
	cfg.LockFile = ""
	for _, dsn := range []string{"", ":memory:", "file:memdb1?mode=memory&cache=shared"} {
		cfg.Database.DSN = dsn
		assert.Empty(t, cfg.LockFilePath(), "dsn %q has no file to lock", dsn)
	}
}

// TestConfigLoadsLockFileFromEnv covers the override an operator actually uses
// to put the lock somewhere the unit's own user can write.
func TestConfigLoadsLockFileFromEnv(t *testing.T) {
	t.Setenv("QUBES_AIR_LOCK_FILE", "/run/qubes-air/console.lock")

	cfg := DefaultConfig()
	cfg.loadFromEnv()

	assert.Equal(t, "/run/qubes-air/console.lock", cfg.LockFile)
	assert.Equal(t, "/run/qubes-air/console.lock", cfg.LockFilePath())
}
