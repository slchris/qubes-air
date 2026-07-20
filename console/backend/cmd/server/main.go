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
	"path/filepath"
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
	"github.com/slchris/qubes-air/console/internal/qrexec"
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

	// agentHealthShutdownGrace is how long in-flight agent probes may finish.
	// Seconds, not minutes: a probe writes one row and abandoning it costs a
	// health reading that the next sweep takes anyway, so there is nothing here
	// worth delaying a restart for.
	agentHealthShutdownGrace = 5 * time.Second
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
	// jobHandler serves the orchestration audit trail.
	jobHandler *handler.JobHandler
	// bootstrapHandler issues a first certificate against a one-shot token.
	// Deliberately NOT mounted under /api/v1 — see setupRouter.
	bootstrapHandler *handler.BootstrapHandler
	// bootstrapTokens mints the tokens cloud-init delivers.
	bootstrapTokens *repository.BootstrapTokenRepository
	// transport is the cross-machine gRPC transport (NoopTransport by default).
	// Held here so it stays a live, injectable dependency; a service will consume
	// it in the next stage-T wiring step.
	transport transport.Transport
	// runner serializes terraform work onto one goroutine. Nil when
	// orchestration is disabled, in which case the service runs inline.
	runner *orchestrator.Runner
	// agents re-probes qube agents in the background so a dead one is noticed
	// without anyone asking. Nil-safe: its methods tolerate a nil receiver.
	agents *service.AgentHealthMonitor
	// certRenewals replaces agent certificates before they expire, over the
	// mTLS channel the agent already holds. Without it the only way to rotate a
	// certificate is to rebuild the qube. Nil-safe.
	certRenewals *service.CertRenewalMonitor
}

// Close releases all resources.
func (d *Dependencies) Close() {
	// Drain orchestration before the database goes away: the completion hook
	// writes a qube's terminal status, and terraform gets a signal (not a kill)
	// so it can persist state rather than stranding VMs and disks.
	if d.runner != nil {
		d.runner.Shutdown(orchestratorShutdownGrace)
	}
	// Probes second, and on a short grace. They must stop before the database
	// closes — a probe writing agent health into a closed handle would log an
	// error on every shutdown — but nothing about a probe is worth waiting for.
	d.agents.Shutdown(agentHealthShutdownGrace)
	// Renewals on the same short grace and for the same reason: an abandoned
	// renewal leaves the agent holding the certificate it already had, so there
	// is nothing to finish and nothing to strand.
	d.certRenewals.Shutdown(agentHealthShutdownGrace)
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

	// The keyring is validated in cfg.Validate() at load time, so a
	// misconfigured key fails startup rather than silently falling back to the
	// insecure default. It supports multiple key versions for rotation
	// (see cmd/rotate-key). Built here because the scheduler needs the
	// credential store to reach the cluster.
	kr, err := cfg.Keyring()
	if err != nil {
		return nil, err
	}
	credentialRepo := repository.NewCredentialRepository(db, kr)
	agentCertRepo := repository.NewAgentCertRepository(db)
	bootstrapTokenRepo := repository.NewBootstrapTokenRepository(db)
	// The snapshot makes the database the source of truth for which qubes
	// exist: the executor renders it to the generated var-file before every
	// terraform invocation, and refuses to act on a qube missing from it.
	// Terraform's provider credentials come from the encrypted credential store
	// too, injected into the subprocess environment. They are deliberately NOT
	// passed as terraform variables: a variable's value is written to state in
	// plaintext, which the state design forbids for long-lived credentials.
	// The agent package is pinned by digest: the artifact store it comes from is
	// unauthenticated plain HTTP, so the hash carried in the identity document
	// is the only thing that makes the download safe to install.
	certIssuer := service.NewCertIssuer(credentialRepo, agentCertRepo,
		cfg.Orchestrator.AgentIdentityDir, cfg.Orchestrator.AgentListen,
		service.AgentPackage{
			AptMirror:         cfg.Orchestrator.AptMirror,
			AptSecurityMirror: cfg.Orchestrator.AptSecurityMirror,
			URL:               cfg.Orchestrator.AgentPackageURL,
			SHA256:            cfg.Orchestrator.AgentPackageSHA256,
			Version:           cfg.Orchestrator.AgentPackageVersion,
		})
	if cfg.Orchestrator.AgentPackageURL == "" {
		log.Printf("WARNING: orchestrator.agent_package_url is not set; " +
			"new qubes will boot without an agent (set QUBES_AIR_AGENT_PACKAGE_URL and _SHA256)")
	}

	exec := buildExecutor(cfg.Orchestrator,
		service.NewQubeSnapshot(qubeRepo, zoneRepo, certIssuer),
		service.NewTerraformEnvFunc(zoneRepo, credentialRepo,
			cfg.Orchestrator.ProxmoxSSHKeyFile, cfg.Orchestrator.ProxmoxSSHUsername))

	// One scheduler instance, shared by placement (qube service) and the
	// capacity endpoint (zone handler).
	clusterScheduler := service.NewClusterScheduler(zoneRepo,
		service.NewZoneCredentialResolver(zoneRepo, credentialRepo))

	// The prober dials each qube's OWN address. It is what makes agent health a
	// per-qube fact; the global transport below is pinned to a single
	// configured endpoint and cannot answer for an arbitrary qube.
	agentProber := service.NewAgentProber(certIssuer, agentCertRepo,
		cfg.Orchestrator.AgentListen,
		time.Duration(cfg.Orchestrator.AgentProbeTimeoutSeconds)*time.Second)

	// Certificate renewal over the agent's existing mTLS channel. Built before
	// the qube service because the service publishes the monitor's warnings into
	// agent health on every probe — a renewal failure recorded only once would be
	// erased by the next successful probe, leaving the fleet reading healthy
	// until the day its certificates ran out.
	certRenewals := service.NewCertRenewalMonitor(
		qubeRepo, agentCertRepo,
		service.NewCertRenewer(certIssuer, certIssuer, agentCertRepo, agentCertRepo,
			cfg.Orchestrator.AgentListen, service.DefaultCertRenewalTimeout),
		qubeRepo,
		service.CertRenewalConfig{
			Interval:  time.Duration(cfg.Orchestrator.AgentCertRenewIntervalSeconds) * time.Second,
			Threshold: float64(cfg.Orchestrator.AgentCertRenewThresholdPercent) / 100,
		})

	qubeSvcOpts := []service.QubeServiceOption{
		service.WithExecutor(exec),
		service.WithTransport(xport),
		service.WithAgentProber(agentProber),
		// Keeps a renewal failure attached to the qube's health on every probe,
		// so it stays visible for the weeks between "renewal broke" and "the
		// certificate expired" instead of for one minute.
		service.WithRenewalWatch(certRenewals),
		// Automatic node selection. Cluster credentials are resolved from the
		// encrypted credential store via the zone's credential_id — never from
		// the environment, so they can be rotated, scoped and audited in one
		// place rather than living in a process's env.
		service.WithPlacementDecider(clusterScheduler),
		// Mint each agent's client certificate at qube creation. The CA lives in
		// the credential store and is created on first use.
		service.WithCertIssuer(certIssuer),
	}

	jobRepo := repository.NewJobRepository(db)
	qubeSvc, runner, agents, jobLogs := startOrchestration(
		cfg.Orchestrator, cfg.JobLogDir(), jobRepo, qubeRepo, zoneRepo, exec, qubeSvcOpts)

	certRenewals.Start()

	// A qube left in a transient status belongs to a job that died with a
	// previous process — the queue is in memory, so nothing will ever finish it.
	// Without this they stay "busy" forever and every later operation is refused.
	reconcileStrandedQubes(context.Background(), qubeRepo)

	// Infrastructure repository and service
	infraRepo := repository.NewInfraRepository(db)
	infraSvc := service.NewInfraService(infraRepo)

	credentialSvc := service.NewCredentialService(credentialRepo)

	// Settings repository and service
	settingsRepo := repository.NewSettingsRepository(db)
	settingsSvc := service.NewSettingsService(settingsRepo)

	return &Dependencies{
		db:                db,
		zoneHandler:       handler.NewZoneHandler(zoneSvc, handler.WithCapacityReader(clusterScheduler)),
		qubeHandler:       handler.NewQubeHandler(qubeSvc, handler.WithCertRepository(agentCertRepo)),
		infraHandler:      handler.NewInfraHandler(infraSvc),
		credentialHandler: handler.NewCredentialHandler(credentialSvc),
		billingHandler:    handler.NewBillingHandler(),
		monitoringHandler: handler.NewMonitoringHandler(),
		settingsHandler:   handler.NewSettingsHandler(settingsSvc),
		jobHandler:        handler.NewJobHandler(jobRepo, jobLogs),
		bootstrapHandler: handler.NewBootstrapHandler(
			bootstrapTokenRepo, certIssuer, agentCertRepo),
		bootstrapTokens: bootstrapTokenRepo,
		transport:       xport,
		runner:          runner,
		agents:          agents,
		certRenewals:    certRenewals,
	}, nil
}

// startOrchestration builds and starts the qube service, the terraform runner
// and the agent-health monitor.
//
// The three are constructed together because they refer to one another: the
// service submits jobs to the runner, the monitor probes through the service,
// and the runner's completion hook schedules a probe on the monitor when an
// apply finishes. That last edge is what forces the ordering below.
//
// The runner turns orchestration asynchronous. Without it the service falls
// back to running terraform inline, which cannot work for real applies: they
// take minutes against a 15s server write deadline.
func startOrchestration(
	cfg config.OrchestratorConfig,
	jobLogDir string,
	jobRepo *repository.JobRepository,
	qubeRepo repository.QubeRepository,
	zoneRepo repository.ZoneRepository,
	exec orchestrator.Executor,
	qubeSvcOpts []service.QubeServiceOption,
) (service.QubeService, *orchestrator.Runner, *service.AgentHealthMonitor, *orchestrator.JobLogStore) {
	// agents is assigned below, once the service it probes through exists, but
	// the completion hook has to be handed to the runner before that. The hook
	// therefore reads it through a getter rather than capturing a value.
	//
	// This is safe without a lock only because runner.Start() is deliberately
	// deferred to the end: creating the worker goroutine is the synchronization
	// point that publishes the write to it.
	var agents *service.AgentHealthMonitor
	var runner *orchestrator.Runner
	var jobLogs *orchestrator.JobLogStore

	if cfg.Enabled {
		// Jobs are persisted, not held in memory: they are the audit record of
		// every infrastructure change this console made.
		if n, err := jobRepo.FailUnfinished(context.Background(),
			"console restarted while this job was in flight; outcome unknown"); err != nil {
			log.Printf("orchestrator: could not reconcile unfinished jobs: %v", err)
		} else if n > 0 {
			log.Printf("orchestrator: marked %d unfinished job(s) failed after restart", n)
		}

		// Job logs live beside the database, under the same data directory that
		// already holds everything else this console must not lose. A failure
		// to create the directory is logged, not fatal: an operator who cannot
		// tail an apply is worse off than one who can, but an operator whose
		// console refuses to start is worse off than either.
		if dir := jobLogDir; dir != "" {
			if store, lerr := orchestrator.NewJobLogStore(dir); lerr == nil {
				jobLogs = store
			} else {
				log.Printf("orchestrator: job logs disabled: %v", lerr)
			}
		}

		// Registers each provisioned qube as a RemoteVM with dom0 over qrexec.
		// Off unless configured, because it needs the dom0 service and policy
		// from mgmt.remotevm.register to exist.
		registrar := service.NewRemoteVMRegistrar(
			qrexec.NewClient(), cfg.RegisterRemoteVM)
		if cfg.RegisterRemoteVM {
			log.Printf("orchestrator: RemoteVM registration enabled (dom0 %s)",
				"qubesair.RegisterRemoteVM")
		}

		runner = orchestrator.NewRunner(orchestrator.RunnerConfig{
			Executor: exec,
			Store:    jobRepo,
			OnDone: makeCompletionHook(qubeRepo,
				func() *service.AgentHealthMonitor { return agents }, registrar),
			Logs: jobLogs,
		})
		qubeSvcOpts = append(qubeSvcOpts, service.WithJobSubmitter(runner))
	}

	qubeSvc := service.NewQubeService(qubeRepo, zoneRepo, qubeSvcOpts...)

	agents = buildAgentHealthMonitor(cfg, qubeRepo, qubeSvc, exec)
	agents.Start()

	// Started only now: its worker calls the completion hook, which reaches
	// `agents`. Starting it before the line above would race that assignment.
	if runner != nil {
		runner.Start()
	}
	return qubeSvc, runner, agents, jobLogs
}

// buildAgentHealthMonitor wires the background agent prober.
//
// exec is passed so the monitor can read a qube's IP address back out of
// terraform: the console has no other source for it, and without an address
// there is nothing to probe. A non-terraform executor simply does not satisfy
// the interface, and the monitor degrades to probing whatever addresses are
// already recorded.
func buildAgentHealthMonitor(
	cfg config.OrchestratorConfig,
	qubeRepo repository.QubeRepository,
	prober service.AgentProbeRunner,
	exec orchestrator.Executor,
) *service.AgentHealthMonitor {
	opts := []service.AgentHealthOption{}
	if reader, ok := exec.(service.AgentAddressReader); ok {
		opts = append(opts, service.WithAgentAddressReader(reader))
	} else {
		log.Printf("agenthealth: this executor cannot report qube addresses; " +
			"qubes with no recorded ip_address will report agent health as unknown")
	}

	return service.NewAgentHealthMonitor(qubeRepo, prober, service.AgentHealthConfig{
		Interval:     time.Duration(cfg.AgentProbeIntervalSeconds) * time.Second,
		SettleBudget: time.Duration(cfg.AgentProbeSettleSeconds) * time.Second,
		SettleRetry:  service.DefaultAgentSettleRetry,
	}, opts...)
}

// makeCompletionHook returns the callback that records a job's outcome on the
// qube. It is the only writer of a terminal status once operations are
// asynchronous: nothing else is still around when terraform finishes.
//
// It is also where the agent probe is triggered, rather than terminalStatusFor
// in the service. Two reasons, both concrete:
//
//   - terminalStatusFor's only remaining caller is the INLINE path, used when no
//     job submitter is configured. That path runs a no-op executor: there is no
//     VM there and nothing to probe.
//   - this hook is the one place that knows a real apply just finished, for
//     which qube, and whether it succeeded. That is exactly the trigger
//     condition, and duplicating it in the service would give two places that
//     have to agree about when a qube became probeable.
//
// The probe is scheduled, never performed here: this runs on the single
// terraform worker goroutine, so waiting for an agent to boot would stall every
// queued apply behind it.
func makeCompletionHook(
	qubeRepo repository.QubeRepository, agents func() *service.AgentHealthMonitor,
	registrar *service.RemoteVMRegistrar,
) orchestrator.Completion {
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

		// Only a successful provision or resume produces a VM that should have
		// a live agent. A failed job, a suspend or a release has nothing to
		// probe, and probing them would fill the health column with failures
		// that mean "this qube is intentionally off".
		// A qube the fleet no longer contains loses its addressing shell. Only
		// on release/destroy, not on suspend: a suspended qube still exists and
		// can be resumed, and dropping its registration would make every resume
		// need a re-register before local qubes could reach it again.
		if j.State == orchestrator.JobSucceeded &&
			(j.Action == orchestrator.ActionRelease || j.Action == orchestrator.ActionDestroy) {
			registrar.DeregisterQuietly(ctx, j.QubeName)
		}

		if j.State != orchestrator.JobSucceeded ||
			(j.Action != orchestrator.ActionProvision && j.Action != orchestrator.ActionResume) {
			return
		}

		// Tell dom0 the machine exists, so local qubes can address it at all.
		// Registration is idempotent, so doing it on resume as well repairs a
		// registration that was lost or never made.
		registrar.RegisterQuietly(ctx, j.QubeName)
		// Note what is NOT happening: the job's outcome is already recorded and
		// is not revisited. The VM exists and the apply did its work, so a
		// silent agent is a fact about the qube, not a failed job.
		agents().Settle(j.QubeID, j.QubeName, string(j.Action))
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
// start/stop only update the DB status — preserving behavior on machines
// without terraform/cloud access. When enabled, a real TerraformExecutor drives
// compute/storage separation.
func buildExecutor(
	cfg config.OrchestratorConfig,
	snapshot orchestrator.QubeSnapshotFunc,
	envFn orchestrator.EnvFunc,
) orchestrator.Executor {
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
	if snapshot != nil {
		opts = append(opts, orchestrator.WithQubeSnapshot(snapshot))
	}
	if envFn != nil {
		opts = append(opts, orchestrator.WithEnvFunc(envFn))
	}
	log.Printf("Orchestrator: ENABLED (terraform_dir=%s, binary=%s, var_file=%s, generated_var_file=%s)",
		cfg.TerraformDir, cfg.TerraformBinary, cfg.VarFile, cfg.GeneratedVarFile)
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

	// Bootstrap sits OUTSIDE /api/v1, and that is the whole design rather than
	// an oversight. An agent on first boot has no API token — it has a one-shot
	// bootstrap token and nothing else — so mounting this behind Auth would make
	// it unreachable by the only caller it exists for. The token in the request
	// body is the authentication, and it authorizes exactly one certificate for
	// exactly one qube, once, before it expires.
	if deps.bootstrapHandler != nil {
		deps.bootstrapHandler.RegisterRoutes(r.Group("/bootstrap"))
	}

	// All /api/v1 routes require a valid Bearer token when an API token is
	// configured. When none is configured, Auth is a pass-through and a
	// warning is logged at startup (see logSecurityWarnings).
	v1 := r.Group("/api/v1")
	v1.Use(middleware.Auth(cfg.Auth.APIToken))
	deps.zoneHandler.RegisterRoutes(v1)
	deps.qubeHandler.RegisterRoutes(v1)
	deps.infraHandler.RegisterRoutes(v1)
	deps.credentialHandler.RegisterRoutes(v1)
	deps.jobHandler.RegisterRoutes(v1)
	deps.billingHandler.RegisterRoutes(v1)
	deps.monitoringHandler.RegisterRoutes(v1)
	deps.settingsHandler.RegisterRoutes(v1)

	v1.GET("/status", statusHandler(deps.db))

	registerWebUI(r, cfg)

	return r
}

// registerWebUI serves the built frontend from cfg.Server.WebRoot, at the same
// origin as the API.
//
// No-op when WebRoot is empty or missing. A console with no UI still manages the
// fleet through /api/v1; refusing to start because a directory of static files
// is absent would turn a cosmetic gap into an outage.
func registerWebUI(r *gin.Engine, cfg *config.Config) {
	root := cfg.Server.WebRoot
	if root == "" {
		return
	}
	index := filepath.Join(root, "index.html")
	if _, err := os.Stat(index); err != nil {
		log.Printf("web UI disabled: %s not readable: %v", index, err)
		return
	}

	r.Static("/assets", filepath.Join(root, "assets"))
	r.StaticFile("/favicon.ico", filepath.Join(root, "favicon.ico"))
	r.GET("/", func(c *gin.Context) { c.File(index) })

	// SPA fallback: the frontend routes client-side, so a reload on /qubes must
	// return index.html rather than 404.
	//
	// The API prefixes are excluded deliberately. Without this, an unknown
	// /api/v1 path would return 200 and a page of HTML, and a caller expecting
	// JSON gets a parse error somewhere far from the wrong URL that caused it —
	// including the frontend's own client, which would report the console as
	// broken rather than the request as misspelled.
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || p == "/health" {
			c.JSON(http.StatusNotFound, gin.H{"code": http.StatusNotFound, "error": "Not Found"})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.JSON(http.StatusNotFound, gin.H{"code": http.StatusNotFound, "error": "Not Found"})
			return
		}
		c.File(index)
	})

	log.Printf("web UI served from %s", root)
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
