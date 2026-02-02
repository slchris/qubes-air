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
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/handler"
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
}

// Dependencies holds all application dependencies.
type Dependencies struct {
	db          *database.DB
	zoneHandler *handler.ZoneHandler
	qubeHandler *handler.QubeHandler
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

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)

	zoneSvc := service.NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := service.NewQubeService(qubeRepo, zoneRepo)

	return &Dependencies{
		db:          db,
		zoneHandler: handler.NewZoneHandler(zoneSvc),
		qubeHandler: handler.NewQubeHandler(qubeSvc),
	}, nil
}

// setupRouter creates and configures the Gin router.
func setupRouter(cfg *config.Config, deps *Dependencies) *gin.Engine {
	gin.SetMode(cfg.Server.Mode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())
	r.Use(corsMiddleware(cfg))

	r.GET("/health", healthHandler(deps.db))

	v1 := r.Group("/api/v1")
	deps.zoneHandler.RegisterRoutes(v1)
	deps.qubeHandler.RegisterRoutes(v1)

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
		c.Header("Access-Control-Allow-Credentials", "true")

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
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server stopped")
}
