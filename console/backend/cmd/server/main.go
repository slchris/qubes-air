// Package main provides the entry point for the Qubes Air console backend.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/handler"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
)

const (
	appName    = "qubes-air-console"
	appVersion = "0.1.0"
)

// Config holds application configuration.
type Config struct {
	Port        string
	DatabaseDSN string
	Mode        string
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("%s v%s starting...", appName, appVersion)

	cfg := loadConfig()

	deps, err := initDependencies(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize: %v", err)
	}
	defer deps.Close()

	router := setupRouter(cfg.Mode, deps)
	runServer(cfg.Port, router)
}

// loadConfig loads configuration from environment variables.
func loadConfig() *Config {
	return &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseDSN: getEnv("DATABASE_DSN", "./qubes-air.db"),
		Mode:        getEnv("GIN_MODE", gin.ReleaseMode),
	}
}

// getEnv retrieves an environment variable or returns default.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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
func initDependencies(cfg *Config) (*Dependencies, error) {
	dbCfg := database.DefaultConfig()
	dbCfg.DSN = cfg.DatabaseDSN

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
func setupRouter(mode string, deps *Dependencies) *gin.Engine {
	gin.SetMode(mode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())
	r.Use(corsMiddleware())

	r.GET("/health", healthHandler(deps.db))

	v1 := r.Group("/api/v1")
	deps.zoneHandler.RegisterRoutes(v1)
	deps.qubeHandler.RegisterRoutes(v1)

	v1.GET("/status", statusHandler(deps.db))

	return r
}

// corsMiddleware adds CORS headers for cross-origin requests.
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
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

// runServer starts the HTTP server with graceful shutdown support.
func runServer(port string, handler http.Handler) {
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("Server listening on port %s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
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
