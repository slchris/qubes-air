/**
 * Qubes Air Console - Type Definitions
 *
 * These types match the backend Go models for type-safe API communication.
 */

// Zone types matching backend models.ZoneType
export type ZoneType = 'proxmox' | 'gcp' | 'aws' | 'azure';

// Zone status values
export type ZoneStatus = 'connected' | 'disconnected';

// Proxmox-specific zone configuration, mirroring backend models.ProxmoxZoneConfig.
//
// This sub-object was missing, which is why the Zones view could not be exposed:
// the backend replaces a zone's config wholesale on update (zone_service.go), so
// a save that omitted these fields erased the node, the template, and — worst —
// the credential link, leaving a "connected" zone that can no longer reach the
// cluster. Every field the backend returns is declared so a round-trip through
// the edit form preserves it.
export interface ProxmoxZoneConfig {
  node?: string;
  datastore_id?: string;
  network_bridge?: string;
  template_vm_id?: number;
  template_node?: string;
  ssh_public_keys?: string[];
  // Reference into the encrypted credential store. Never the secret itself.
  credential_id?: string;
}

// GCP-specific zone configuration, mirroring backend models.GCPZoneConfig.
// Same rule as the proxmox sub-object: placement plus a credential REFERENCE,
// never the secret — ZoneConfig is returned by the zones API in cleartext.
export interface GCPZoneConfig {
  // Instances and their data disks must share a compute zone or the disk
  // cannot be attached.
  zone?: string;
  // GCP has images, not template VMs, so this replaces template_vm_id.
  source_image?: string;
  // A PRIVATE bucket the per-qube agent identity is delivered through. It
  // cannot go in instance metadata: metadata is a resource attribute, so
  // terraform would write the agent's private key into state.
  identity_bucket?: string;
  service_account_email?: string;
  network?: string;
  subnetwork?: string;
  // Exposes the agent's mTLS port to the internet.
  assign_public_ip?: boolean;
  credential_id?: string;
}

// Zone configuration
export interface ZoneConfig {
  endpoint: string;
  region?: string;
  project?: string;
  // Present on proxmox zones. Carried through edits verbatim — see the note on
  // ProxmoxZoneConfig for why dropping it is destructive.
  proxmox?: ProxmoxZoneConfig;
  // Present on gcp zones, same contract.
  gcp?: GCPZoneConfig;
}

// Zone entity matching backend models.Zone
export interface Zone {
  id: string;
  name: string;
  type: ZoneType;
  status: ZoneStatus;
  config: ZoneConfig;
  created_at: string;
  updated_at: string;
}

// Request payload for creating a zone
export interface ZoneCreateRequest {
  name: string;
  type: ZoneType;
  config: ZoneConfig;
}

// Request payload for updating a zone
export interface ZoneUpdateRequest {
  name?: string;
  config?: ZoneConfig;
}

// Qube types matching backend models.QubeType
export type QubeType = 'app' | 'work' | 'dev' | 'gpu' | 'disp' | 'sys';

// Qube status values matching backend models.QubeStatus.
//
// 'suspended' and 'released' both mean the compute VM is gone while the
// persistent data disk remains — the difference is intent: suspended was parked
// to save cost, released was deleted by the user and is awaiting a purge.
// 'purged' is terminal: the data disk is gone and the identity revoked.
//
// The four transient values mean a terraform job is queued or running. They are
// not decoration: the backend refuses a second operation while a qube is in one,
// so the UI must show them rather than appear unresponsive for several minutes.
export type QubeStatus =
  | 'pending'
  | 'creating'
  | 'running'
  | 'stopped'
  | 'suspended'
  | 'released'
  | 'purged'
  | 'resuming'
  | 'suspending'
  | 'deleting'
  | 'error';

/** Statuses for which a terraform job is in flight. */
export const TRANSIENT_QUBE_STATUSES: readonly QubeStatus[] = [
  'creating',
  'resuming',
  'suspending',
  'deleting',
];

/** Reports whether an operation is currently running for this qube. */
export function isTransientStatus(status: QubeStatus): boolean {
  return TRANSIENT_QUBE_STATUSES.includes(status);
}

// Statuses where a compute instance can exist, and therefore where the agent
// health readings describe something live.
//
// Mirrors computeRunning in console/backend/internal/service/qube_predicates.go:
// running | creating | resuming. The prober and the certificate reissue path ask
// that predicate, so nothing else can produce a fresh agent reading; a parked
// qube (suspended/released/stopped) keeps whatever was recorded before it was
// parked, and that stale reading must not be rendered as a live problem.
export const COMPUTE_BEARING_QUBE_STATUSES: readonly QubeStatus[] = [
  'running',
  'creating',
  'resuming',
];

/** Reports whether this qube can have a compute instance, hence a probed agent. */
export function hasCompute(status: QubeStatus): boolean {
  return COMPUTE_BEARING_QUBE_STATUSES.includes(status);
}

// Qube specifications
export interface QubeSpec {
  vcpu: number;
  memory: number;
  /** OS/root disk in GB. Recreated with the compute instance. */
  disk: number;
  /** Persistent data disk in GB. Survives suspend/resume. */
  data_disk_gb?: number;
  /** Pins the qube to a cluster node. Empty means the zone default. */
  node?: string;
}

// Qube entity matching backend models.Qube
export interface Qube {
  purge_requested: boolean;
  id: string;
  name: string;
  zone_id?: string;  // Optional: qube can exist without a zone
  zone_name?: string;
  type: QubeType;
  status: QubeStatus;
  spec: QubeSpec;
  ip_address?: string;
  created_at: string;
  updated_at: string;
  // Agent health, returned by the API and previously undeclared here, which is
  // why the card could not show it. The distinction this carries is the whole
  // point of the field: a qube can be "running" while its agent is unreachable,
  // and the card was painting a green dot for exactly that case.
  agent_health?: AgentHealth;
  agent_last_probed_at?: string;
  agent_last_healthy_at?: string;
  // The reason the last probe failed. Empty when healthy.
  agent_last_error?: string;
  // When the current run of failed probes began. Absent while the agent answers.
  agent_failing_since?: string;
  // Derived by the backend when the row is read — never stored. See AgentRecovery.
  agent_recovery?: AgentRecovery;
}

// Agent health as reported by the console's background prober.
//
// These strings must match models.AgentHealth on the backend exactly. They did
// not: this declared 'unhealthy' while the API returns 'unreachable', so every
// comparison against it was false — three qubes with dead agents produced no
// warning anywhere, which is precisely the "looks fine" failure the field
// exists to prevent.
//   healthy      — last probe succeeded
//   unreachable  — last probe failed (see agent_last_error)
//   unknown      — not probed yet, or probing disabled
export type AgentHealth = 'healthy' | 'unreachable' | 'unknown';

// Whether an agent that is not answering can still come back without a human.
//
// These strings must match models.AgentRecovery on the backend exactly.
//   none    — nothing is failing, or the console has no verdict yet
//   pending — failing, but for less than the agent unit's own restart budget, so
//             an automatic restart may still be in flight
//   manual  — the unit has stopped restarting itself within this boot, so
//             waiting will not fix it: it needs `systemctl reset-failed`
//
// "manual" is the console's reading of how long nothing has answered, NOT a
// look inside the guest: the systemd unit is invisible from here, and a
// filtered port or a package that was never installed read exactly the same.
// The unit is enabled, so a reboot starts it again — with the same unfixed
// cause it will fail again. Recovery steps: docs/runbook-remotevm.md.
export type AgentRecovery = 'none' | 'pending' | 'manual';

// Request payload for creating a qube
export interface QubeCreateRequest {
  name: string;
  zone_id?: string;  // Optional: qube can exist without a zone
  type: QubeType;
  spec?: Partial<QubeSpec>;
}

// Request payload for updating a qube
export interface QubeUpdateRequest {
  name?: string;
  spec?: Partial<QubeSpec>;
}

// Generic list response wrapper
export interface ListResponse<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

// Zone list response
export interface ZoneListResponse {
  zones: Zone[];
  total: number;
}

// Qube list response
export interface QubeListResponse {
  qubes: Qube[];
  total: number;
}

// Error response from API
export interface ApiError {
  // The HTTP status text ("Bad Request"), not the reason. The reason is in
  // `message` — see api.ts's errorMessage().
  error: string;
  // Why the request was refused, in the console's own words (e.g. which spec
  // bound was broken). Optional because a proxy, or an error raised before the
  // console's own handler, can return a body without it.
  message?: string;
  code?: string;
  details?: Record<string, string>;
}

// Health check response. Mirrors the console's GET /health body exactly: the
// server sends one shape whether or not it is healthy, so a probe never parses
// two schemas. `worker.dispatcher` is "disabled" when orchestration is off,
// which is a configuration rather than an outage.
//
// The four build fields are what `qubes-air-console --version` prints for the
// same binary, names included. "unknown" in any of them means the binary was
// built without the version stamps (-X .../internal/buildinfo.*), not that the
// version is empty — see docs/upgrade-rollback.md §3.1.
export interface HealthResponse {
  status: string;
  database: string;
  worker: {
    dispatcher: string;
    queued?: number;
    running?: number;
  };
  version: string;
  revision: string;
  build_time: string;
  tree: 'clean' | 'dirty' | 'unknown';
}

// Status response
export interface StatusResponse {
  app: string;
  version: string;
  zones_count: number;
  qubes_count: number;
}

// List options for filtering and pagination
export interface ListOptions {
  page?: number;
  pageSize?: number;
  status?: string;
  type?: string;
  zoneId?: string;
}


// ============================================================================
// Orchestration jobs
// ============================================================================

/** What a job does to infrastructure. */
export type JobAction = 'provision' | 'resume' | 'suspend' | 'release' | 'destroy';

/** Lifecycle of a single terraform invocation. */
export type JobState = 'queued' | 'running' | 'succeeded' | 'failed';

/**
 * One orchestration job — both the poll target for a 202 response and a row in
 * the audit trail of every infrastructure change the console made.
 */
export interface Job {
  id: string;
  qube_id: string;
  qube_name: string;
  action: JobAction;
  state: JobState;
  error?: string;
  enqueued_at: string;
  started_at?: string;
  finished_at?: string;
}

/** Response from GET /jobs. */
export interface JobListResponse {
  jobs: Job[];
  count: number;
}

/**
 * What a mutating qube endpoint returns. The work is NOT done when this
 * arrives: a real terraform apply takes minutes, so the qube comes back in a
 * transient status and job_id is what to poll.
 */
export interface Operation {
  qube: Qube;
  job_id?: string;
}

/** Reports whether a job has reached a terminal state. */
export function isJobFinished(job: Job): boolean {
  return job.state === 'succeeded' || job.state === 'failed';
}


// ============================================================================
// Cluster capacity
// ============================================================================

/** One cluster node's live capacity. */
export interface NodeInfo {
  name: string;
  online: boolean;
  max_cpu: number;
  /** Current load as a fraction (0..1). */
  cpu_usage: number;
  mem_used_bytes: number;
  mem_total_bytes: number;
  mem_free_bytes: number;
}

/**
 * How a provider expresses "can I fit another workload?".
 *
 * The two kinds ask genuinely different questions. A node pool is a finite set
 * of machines you own, where placement is bin-packing against free memory. An
 * elastic provider decides the machine itself and never tells you which, so the
 * binding constraints are quota and cost — there is no node to pick.
 */
export type CapacityKind = 'node_pool' | 'quota' | 'unknown';

/** Elastic-provider usage against account limits. */
export interface QuotaInfo {
  instances_used: number;
  instances_limit?: number;
  vcpu_used: number;
  vcpu_limit?: number;
  memory_mb_used: number;
  month_to_date_usd?: number;
  hourly_rate_usd?: number;
}

/**
 * Response from GET /zones/:id/capacity. Exactly one of nodes/quota is
 * populated, selected by kind.
 */
export interface ZoneCapacity {
  kind: CapacityKind;
  nodes?: NodeInfo[];
  quota?: QuotaInfo;
  note?: string;
}

/**
 * Fraction of a node's memory the scheduler keeps unused. Mirrors the backend
 * so the UI can show the same eligibility the scheduler will apply, rather than
 * a raw free-memory figure that would suggest a node fits when it does not.
 */
export const SCHEDULER_HEADROOM = 0.15;

/** Reports whether a node could take a guest of the given size. */
export function nodeCanFit(node: NodeInfo, memoryMB: number): boolean {
  if (!node.online) return false;
  const reserve = node.mem_total_bytes * SCHEDULER_HEADROOM;
  return node.mem_free_bytes - reserve >= memoryMB * 1024 * 1024;
}
