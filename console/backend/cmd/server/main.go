// Package main provides the entry point for the Qubes Air console backend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/handler"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
)

const (
	appName    = "qubes-air-console"
	appVersion = "0.1.0"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Parse command line flags
	configPath := flag.String("config", "", "Path to configuration file (YAML)")
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("%s v%s\n", appName, appVersion)
		os.Exit(0)
	}

	log.Printf("%s v%s starting...", appName, appVersion)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	logConfig(cfg)
	logSecurityWarnings(cfg)

	// Initialize dependencies
	deps, err := initDependencies(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize: %v", err)
	}
	defer deps.Close()

	// Setup router and run server
	router := setupRouter(cfg, deps)
	runServer(cfg, router)
}

// logConfig logs the current configuration (without sensitive data).
func logConfig(cfg *config.Config) {
	log.Printf("Configuration:")
	log.Printf("  Listen: %s", cfg.Address())
	log.Printf("  TLS: %v", cfg.IsTLSEnabled())
	log.Printf("  Database: %s", cfg.Database.DSN)
	log.Printf("  CORS Origins: %v", cfg.CORS.AllowedOrigins)
	log.Printf("  Auth: %v", authStatus(cfg))
}

// authStatus returns a human-readable auth status for logging.
func authStatus(cfg *config.Config) string {
	if cfg.IsAuthEnabled() {
		return "enabled (Bearer token)"
	}
	return "DISABLED"
}

// logSecurityWarnings emits prominent warnings for insecure configurations so
// that an operator does not unknowingly expose the console.
func logSecurityWarnings(cfg *config.Config) {
	if !cfg.IsAuthEnabled() {
		log.Printf("SECURITY WARNING: API authentication is DISABLED. " +
			"All /api/v1 endpoints (including credential management) are open. " +
			"Set auth.api_token (or QUBES_AIR_API_TOKEN) before exposing beyond localhost.")
	}
	if cfg.UsesDevEncryptionKey() {
		log.Printf("SECURITY WARNING: using the built-in development encryption key. " +
			"Stored credential secrets are NOT securely encrypted. " +
			"Set a real 32-byte security.encryption_key (or QUBES_AIR_ENCRYPTION_KEY) in production.")
	}
	if slices.Contains(cfg.CORS.AllowedOrigins, "*") {
		log.Printf("SECURITY WARNING: CORS allows all origins (\"*\"). " +
			"Restrict cors.allowed_origins (or QUBES_AIR_CORS_ORIGINS) in production.")
	}
}

// Dependencies holds all application dependencies.
type Dependencies struct {
	db                *database.DB
	zoneHandler       *handler.ZoneHandler
	qubeHandler       *handler.QubeHandler
	infraHandler      *handler.InfraHandler
	credentialHandler *handler.CredentialHandler
	billingHandler    *handler.BillingHandler
	monitoringHandler *handler.MonitoringHandler
	settingsHandler   *handler.SettingsHandler
}

// Close releases all resources.
func (d *Dependencies) Close() {
	if d.db != nil {
		if err := d.db.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		}
	}
}

// initDependencies creates and wires all application dependencies.
func initDependencies(cfg *config.Config) (*Dependencies, error) {
	dbCfg := database.DefaultConfig()
	dbCfg.DSN = cfg.Database.DSN

	db, err := database.New(dbCfg)
	if err != nil {
		return nil, err
	}

	// Zone and Qube repositories and services
	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := service.NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := service.NewQubeService(qubeRepo, zoneRepo)

	// Infrastructure repository and service
	infraRepo := repository.NewInfraRepository(db)
	infraSvc := service.NewInfraService(infraRepo)

	// Credential repository and service. The key is validated in cfg.Validate()
	// at load time, so a misconfigured key fails startup rather than silently
	// falling back to the insecure default.
	encryptionKey, err := cfg.EncryptionKeyBytes()
	if err != nil {
		return nil, err
	}
	credentialRepo := repository.NewCredentialRepository(db, encryptionKey)
	credentialSvc := service.NewCredentialService(credentialRepo)

	// Settings repository and service
	settingsRepo := repository.NewSettingsRepository(db)
	settingsSvc := service.NewSettingsService(settingsRepo)

	return &Dependencies{
		db:                db,
		zoneHandler:       handler.NewZoneHandler(zoneSvc),
		qubeHandler:       handler.NewQubeHandler(qubeSvc),
		infraHandler:      handler.NewInfraHandler(infraSvc),
		credentialHandler: handler.NewCredentialHandler(credentialSvc),
		billingHandler:    handler.NewBillingHandler(),
		monitoringHandler: handler.NewMonitoringHandler(),
		settingsHandler:   handler.NewSettingsHandler(settingsSvc),
	}, nil
}

// setupRouter creates and configures the Gin router.
func setupRouter(cfg *config.Config, deps *Dependencies) *gin.Engine {
	gin.SetMode(cfg.Server.Mode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())
	r.Use(corsMiddleware(cfg))

	// /health is intentionally left unauthenticated for liveness probes.
	r.GET("/health", healthHandler(deps.db))

	// All /api/v1 routes require a valid Bearer token when an API token is
	// configured. When none is configured, Auth is a pass-through and a
	// warning is logged at startup (see logSecurityWarnings).
	v1 := r.Group("/api/v1")
	v1.Use(middleware.Auth(cfg.Auth.APIToken))
	deps.zoneHandler.RegisterRoutes(v1)
	deps.qubeHandler.RegisterRoutes(v1)
	deps.infraHandler.RegisterRoutes(v1)
	deps.credentialHandler.RegisterRoutes(v1)
	deps.billingHandler.RegisterRoutes(v1)
	deps.monitoringHandler.RegisterRoutes(v1)
	deps.settingsHandler.RegisterRoutes(v1)

	v1.GET("/status", statusHandler(deps.db))

	return r
}

// corsMiddleware adds CORS headers based on configuration.
func corsMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		allowedOrigin := getAllowedOrigin(origin, cfg.CORS.AllowedOrigins)

		c.Header("Access-Control-Allow-Origin", allowedOrigin)
		c.Header("Access-Control-Allow-Methods", strings.Join(cfg.CORS.AllowedMethods, ", "))
		c.Header("Access-Control-Allow-Headers", strings.Join(cfg.CORS.AllowedHeaders, ", "))
		// Per the CORS spec, "Allow-Credentials: true" MUST NOT be combined
		// with a wildcard origin. Only advertise credentials support when a
		// specific origin is echoed back.
		if allowedOrigin != "*" && allowedOrigin != "" {
			c.Header("Access-Control-Allow-Credentials", "true")
		}

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// getAllowedOrigin checks if the origin is allowed and returns appropriate value.
func getAllowedOrigin(origin string, allowedOrigins []string) string {
	for _, allowed := range allowedOrigins {
		if allowed == "*" {
			return "*"
		}
		if allowed == origin {
			return origin
		}
	}
	// If origin not in list, return first allowed origin or empty
	if len(allowedOrigins) > 0 {
		return allowedOrigins[0]
	}
	return ""
}

// healthHandler returns a health check endpoint handler.
func healthHandler(db *database.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := "healthy"
		dbStatus := "connected"

		if err := db.HealthCheck(c.Request.Context()); err != nil {
			status = "unhealthy"
			dbStatus = "disconnected"
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status":   status,
				"database": dbStatus,
				"version":  appVersion,
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":   status,
			"database": dbStatus,
			"version":  appVersion,
		})
	}
}

// statusHandler returns system status information.
func statusHandler(db *database.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"version": appVersion,
			"name":    appName,
		})
	}
}

// runServer starts the HTTP/HTTPS server with graceful shutdown support.
func runServer(cfg *config.Config, handler http.Handler) {
	srv := &http.Server{
		Addr:         cfg.Address(),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if cfg.IsTLSEnabled() {
			log.Printf("Server listening on https://%s", cfg.Address())
			if err := srv.ListenAndServeTLS(
				cfg.Server.TLS.CertFile,
				cfg.Server.TLS.KeyFile,
			); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Server error: %v", err)
			}
		} else {
			log.Printf("Server listening on http://%s", cfg.Address())
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Server error: %v", err)
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server stopped")
}
