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

// Qube status values matching backend models.QubeStatus
export type QubeStatus = 'pending' | 'creating' | 'running' | 'stopped' | 'error';

// Qube specifications
export interface QubeSpec {
  vcpu: number;
  memory: number;
  disk: number;
  template: string;
}

// Qube entity matching backend models.Qube
export interface Qube {
  id: string;
  name: string;
  zone_id: string;
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
  zone_id: string;
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
