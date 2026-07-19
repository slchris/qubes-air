/**
 * Qubes Air Console - Type Definitions
 *
 * These types match the backend Go models for type-safe API communication.
 */

// Zone types matching backend models.ZoneType
export type ZoneType = 'proxmox' | 'gcp' | 'aws' | 'azure';

// Zone status values
export type ZoneStatus = 'connected' | 'disconnected';

// Zone configuration
export interface ZoneConfig {
  endpoint: string;
  region?: string;
  project?: string;
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
}

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
  error: string;
  code?: string;
  details?: Record<string, string>;
}

// Health check response
export interface HealthResponse {
  status: string;
  database: string;
  timestamp: string;
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
