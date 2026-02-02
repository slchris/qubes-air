package config

import (
	"os"
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
