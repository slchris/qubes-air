// Package config provides application configuration management.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	CORS     CORSConfig     `yaml:"cors"`
	Security SecurityConfig `yaml:"security"`
	Auth     AuthConfig     `yaml:"auth"`
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
	EncryptionKey string `yaml:"encryption_key"`
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
func (c *Config) UsesDevEncryptionKey() bool {
	return c.Security.EncryptionKey == "" || c.Security.EncryptionKey == devEncryptionKey
}

// EncryptionKeyBytes returns the 32-byte AES key. It returns an error if a key
// is configured but is not exactly 32 bytes, so that a misconfiguration fails
// fast at startup rather than silently falling back to the insecure default.
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
	if token := os.Getenv("QUBES_AIR_API_TOKEN"); token != "" {
		c.Auth.APIToken = token
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
	// falling back to the insecure default.
	if _, err := c.EncryptionKeyBytes(); err != nil {
		return err
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
