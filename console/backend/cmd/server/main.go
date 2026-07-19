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

	"crypto/tls"
	"crypto/x509"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/handler"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/slchris/qubes-air/console/internal/transport"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

const (
	appName    = "qubes-air-console"
	appVersion = "0.1.0"

	// orchestratorShutdownGrace is how long a terraform job may finish during
	// shutdown. A real apply takes minutes; cutting one short is what leaves
	// infrastructure that terraform has no record of.
	orchestratorShutdownGrace = 10 * time.Minute
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
	// transport is the cross-machine gRPC transport (NoopTransport by default).
	// Held here so it stays a live, injectable dependency; a service will consume
	// it in the next stage-T wiring step.
	transport transport.Transport
	// runner serializes terraform work onto one goroutine. Nil when
	// orchestration is disabled, in which case the service runs inline.
	runner *orchestrator.Runner
}

// Close releases all resources.
func (d *Dependencies) Close() {
	// Drain orchestration before the database goes away: the completion hook
	// writes a qube's terminal status, and terraform gets a signal (not a kill)
	// so it can persist state rather than stranding VMs and disks.
	if d.runner != nil {
		d.runner.Shutdown(orchestratorShutdownGrace)
	}
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

	// Cross-machine gRPC transport (NoopTransport by default). Built once and
	// shared: consumed by QubeService.CheckReachable and held on Dependencies.
	xport := buildTransport(context.Background(), cfg.Transport)

	// Zone and Qube repositories and services
	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := service.NewZoneService(zoneRepo, qubeRepo)
	exec := buildExecutor(cfg.Orchestrator)

	// The runner turns orchestration asynchronous. Without it the service falls
	// back to running terraform inline, which cannot work for real applies: they
	// take minutes against a 15s server write deadline.
	var runner *orchestrator.Runner
	qubeSvcOpts := []service.QubeServiceOption{
		service.WithExecutor(exec),
		service.WithTransport(xport),
	}
	if cfg.Orchestrator.Enabled {
		runner = orchestrator.NewRunner(orchestrator.RunnerConfig{
			Executor: exec,
			Store:    orchestrator.NewMemoryJobStore(),
			OnDone:   makeCompletionHook(qubeRepo),
		})
		runner.Start()
		qubeSvcOpts = append(qubeSvcOpts, service.WithJobSubmitter(runner))
	}

	qubeSvc := service.NewQubeService(qubeRepo, zoneRepo, qubeSvcOpts...)

	// A qube left in a transient status belongs to a job that died with a
	// previous process — the queue is in memory, so nothing will ever finish it.
	// Without this they stay "busy" forever and every later operation is refused.
	reconcileStrandedQubes(context.Background(), qubeRepo)

	// Infrastructure repository and service
	infraRepo := repository.NewInfraRepository(db)
	infraSvc := service.NewInfraService(infraRepo)

	// Credential repository and service. The keyring is validated in
	// cfg.Validate() at load time, so a misconfigured key fails startup rather
	// than silently falling back to the insecure default. The keyring supports
	// multiple key versions for rotation (see cmd/rotate-key).
	kr, err := cfg.Keyring()
	if err != nil {
		return nil, err
	}
	credentialRepo := repository.NewCredentialRepository(db, kr)
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
		transport:         xport,
		runner:            runner,
	}, nil
}

// makeCompletionHook returns the callback that records a job's outcome on the
// qube. It is the only writer of a terminal status once operations are
// asynchronous: nothing else is still around when terraform finishes.
func makeCompletionHook(qubeRepo repository.QubeRepository) orchestrator.Completion {
	return func(ctx context.Context, j *orchestrator.Job) {
		status := models.QubeStatusError
		if j.State == orchestrator.JobSucceeded {
			switch j.Action {
			case orchestrator.ActionProvision, orchestrator.ActionResume:
				status = models.QubeStatusRunning
			case orchestrator.ActionSuspend:
				status = models.QubeStatusSuspended
			case orchestrator.ActionRelease, orchestrator.ActionDestroy:
				status = models.QubeStatusReleased
			}
		}
		if err := qubeRepo.UpdateStatus(ctx, j.QubeID, status); err != nil {
			log.Printf("orchestrator: job %s finished (%s) but recording status %q failed: %v",
				j.ID, j.State, status, err)
		}
	}
}

// reconcileStrandedQubes clears transient statuses left behind by a process
// that died mid-operation. The real infrastructure state is unknown at this
// point, so they are marked error rather than guessed at — error is a valid
// source status, so the operator can simply retry.
func reconcileStrandedQubes(ctx context.Context, qubeRepo repository.QubeRepository) {
	transient := []models.QubeStatus{
		models.QubeStatusCreating, models.QubeStatusResuming,
		models.QubeStatusSuspending, models.QubeStatusDeleting,
	}
	stranded, err := qubeRepo.ListByStatus(ctx, transient)
	if err != nil {
		log.Printf("orchestrator: could not scan for stranded qubes: %v", err)
		return
	}
	for _, q := range stranded {
		log.Printf("orchestrator: qube %q was left in %q by a previous process; marking error for retry",
			q.Name, q.Status)
		if err := qubeRepo.UpdateStatus(ctx, q.ID, models.QubeStatusError); err != nil {
			log.Printf("orchestrator: reconciling qube %q failed: %v", q.Name, err)
		}
	}
}

// buildExecutor selects the orchestration executor from configuration. When
// orchestration is disabled (the default), a NoopExecutor is returned so that
// start/stop only update the DB status — preserving behaviour on machines
// without terraform/cloud access. When enabled, a real TerraformExecutor drives
// compute/storage separation.
func buildExecutor(cfg config.OrchestratorConfig) orchestrator.Executor {
	if !cfg.Enabled {
		log.Printf("Orchestrator: DISABLED (start/stop only update DB status; " +
			"set orchestrator.enabled=true and orchestrator.terraform_dir to drive terraform)")
		return orchestrator.NewNoopExecutor()
	}

	opts := []orchestrator.TerraformOption{}
	if cfg.TerraformBinary != "" {
		opts = append(opts, orchestrator.WithBinary(cfg.TerraformBinary))
	}
	if cfg.VarFile != "" {
		opts = append(opts, orchestrator.WithVarFile(cfg.VarFile))
	}
	if cfg.GeneratedVarFile != "" {
		opts = append(opts, orchestrator.WithGeneratedVarFile(cfg.GeneratedVarFile))
	}
	log.Printf("Orchestrator: ENABLED (terraform_dir=%s, binary=%s, var_file=%s, generated_var_file=%s)",
		cfg.TerraformDir, cfg.TerraformBinary, cfg.VarFile, cfg.GeneratedVarFile)
	// NOTE: WithQubeSnapshot is not wired yet. Until it is, the generated
	// var-file is never refreshed from the database and the console cannot
	// create qubes terraform knows about — see the Create/Delete wiring work.
	return orchestrator.NewTerraformExecutor(cfg.TerraformDir, opts...)
}

// buildTransport wires the cross-machine gRPC transport. Disabled by default it
// returns a NoopTransport. When enabled it obtains mTLS material (from vault via
// qrexec, or from files), wires the reverse handler (routes remote→local calls
// to the local dom0, policy C: ask), starts the outbound dial loop, and returns
// the gRPC client (a transport.Transport).
//
// The returned Transport is a first-class injectable dependency; a service will
// consume it for cross-machine qrexec in the next wiring step (design §6).
func buildTransport(ctx context.Context, cfg config.TransportConfig) transport.Transport {
	if !cfg.Enabled {
		log.Printf("Transport: DISABLED (no gRPC transport; set transport.enabled=true, " +
			"transport.remote_endpoint and mTLS material to drive cross-machine qrexec)")
		return transport.NoopTransport{}
	}

	tlsCfg, err := obtainClientMTLS(ctx, cfg)
	if err != nil {
		log.Printf("Transport: mTLS setup failed (%v); falling back to NoopTransport", err)
		return transport.NoopTransport{}
	}

	// Reverse handler: deliver remote→local calls to the configured local target
	// (e.g. vault-cloud), gated by local dom0 policy C (ask). nil disables reverse.
	reverse := transportgrpc.NewReverseHandler(transportgrpc.ReverseConfig{
		LocalTarget: cfg.ReverseLocalTarget,
	})

	client := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: cfg.RemoteEndpoint,
		RelayName:      cfg.RelayName,
		RemoteName:     cfg.RemoteName,
		KeepAlive:      time.Duration(cfg.KeepAliveSeconds) * time.Second,
		ReconnectMin:   time.Duration(cfg.ReconnectMinSeconds) * time.Second,
		ReconnectMax:   time.Duration(cfg.ReconnectMaxSeconds) * time.Second,
		TLS:            tlsCfg,
	}, reverse)

	go func() {
		if err := client.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("Transport: gRPC client stopped: %v", err)
		}
	}()
	log.Printf("Transport: ENABLED (gRPC → %s, relay=%s, remote=%s, reverse_target=%q)",
		cfg.RemoteEndpoint, cfg.RelayName, cfg.RemoteName, cfg.ReverseLocalTarget)
	return client
}

// obtainClientMTLS gets the client TLS config either from vault-cloud (via
// qrexec ask, in memory) when VaultCerts is set, or from the configured files.
func obtainClientMTLS(ctx context.Context, cfg config.TransportConfig) (*tls.Config, error) {
	if cfg.VaultCerts {
		return transportgrpc.FetchClientMTLS(ctx, transportgrpc.VaultCertConfig{
			VaultQube:  cfg.VaultQube,
			CertName:   cfg.VaultCertName,
			KeyName:    cfg.VaultKeyName,
			CAName:     cfg.VaultCAName,
			ServerName: cfg.RemoteName,
		})
	}
	return loadClientMTLS(cfg)
}

// loadClientMTLS builds the client TLS config from the configured cert/key/CA
// file paths (provisioned from vault-cloud via qrexec ask; this only reads the
// paths). CAFile is optional — if empty, the system roots are used.
func loadClientMTLS(cfg config.TransportConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %q has no valid certificates", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
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
func statusHandler(_ *database.DB) gin.HandlerFunc {
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
