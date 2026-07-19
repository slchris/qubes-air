/**
 * Qubes Air Console - API Service
 *
 * Type-safe API client following Google style guidelines.
 * All functions have single responsibility and minimal cyclomatic complexity.
 */

import type {
  Zone,
  ZoneCreateRequest,
  ZoneUpdateRequest,
  ZoneListResponse,
  Qube,
  QubeCreateRequest,
  QubeUpdateRequest,
  QubeListResponse,
  Job,
  JobListResponse,
  NodeListResponse,
  Operation,
  ListOptions,
  HealthResponse,
  StatusResponse,
  ApiError,
} from './types';

/**
 * Get the API base URL from environment or use default.
 * In production, this uses the same origin.
 * In development, it can be configured via VITE_API_BASE_URL.
 */
export function getApiBaseUrl(): string {
  // Check for environment variable (Vite injects VITE_ prefixed vars)
  const envApiBase = import.meta.env.VITE_API_BASE_URL;
  if (envApiBase) {
    return envApiBase;
  }
  // Default to /api/v1 prefix
  return '/api/v1';
}

const API_BASE = getApiBaseUrl();

/**
 * localStorage key holding the API bearer token.
 *
 * The backend accepts a single static token (see middleware.Auth). When it is
 * configured server-side, EVERY /api/v1 request must carry it — without this
 * the whole console 401s the moment an operator secures their deployment.
 */
const AUTH_TOKEN_KEY = 'qubesair.apiToken';

/** Returns the stored API token, or null when none is set. */
export function getApiToken(): string | null {
  try {
    return localStorage.getItem(AUTH_TOKEN_KEY);
  } catch {
    // localStorage can throw in private-browsing or sandboxed contexts.
    return null;
  }
}

/** Stores the API token, or clears it when given an empty value. */
export function setApiToken(token: string): void {
  try {
    if (token) {
      localStorage.setItem(AUTH_TOKEN_KEY, token);
    } else {
      localStorage.removeItem(AUTH_TOKEN_KEY);
    }
  } catch {
    // Non-fatal: the request layer simply keeps sending unauthenticated calls.
  }
}

/**
 * Builds request headers, attaching the bearer token when one is configured.
 *
 * Every request funnels through here precisely so authentication cannot be
 * forgotten on one verb — which is exactly how the client ended up sending no
 * Authorization header at all.
 */
function buildHeaders(hasBody: boolean): HeadersInit {
  const headers: Record<string, string> = {};
  if (hasBody) {
    headers['Content-Type'] = 'application/json';
  }
  const token = getApiToken();
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }
  return headers;
}

/**
 * Custom error class for API errors.
 */
export class ApiException extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details?: Record<string, string>
  ) {
    super(message);
    this.name = 'ApiException';
  }
}

/**
 * Builds query string from list options.
 */
function buildQueryString(options?: ListOptions): string {
  if (!options) return '';

  const params = new URLSearchParams();
  if (options.page) params.set('page', String(options.page));
  if (options.pageSize) params.set('page_size', String(options.pageSize));
  if (options.status) params.set('status', options.status);
  if (options.type) params.set('type', options.type);
  if (options.zoneId) params.set('zone_id', options.zoneId);

  const query = params.toString();
  return query ? `?${query}` : '';
}

/**
 * Handles API response and throws on error.
 */
async function handleResponse<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const error = await parseErrorResponse(response);
    throw new ApiException(
      response.status,
      error.code ?? 'UNKNOWN_ERROR',
      error.error,
      error.details
    );
  }
  return response.json() as Promise<T>;
}

/**
 * Parses error response body.
 */
async function parseErrorResponse(response: Response): Promise<ApiError> {
  try {
    return await response.json();
  } catch {
    return { error: response.statusText };
  }
}

/**
 * Authenticated fetch for callers that need the raw Response.
 *
 * Components that talk to endpoints without a typed wrapper MUST use this
 * rather than calling fetch directly — several did, which meant they kept
 * sending unauthenticated requests even after the client learned to
 * authenticate. Takes a path relative to the API base.
 */
export async function apiFetch(path: string, init?: RequestInit): Promise<Response> {
  const hasBody = init?.body !== undefined;
  return fetch(`${API_BASE}${path}`, {
    ...init,
    headers: { ...buildHeaders(hasBody), ...(init?.headers ?? {}) },
  });
}

/**
 * Single exit point for every API call. Centralising this is what guarantees
 * the Authorization header is attached uniformly.
 */
async function request<T>(method: string, path: string, body?: unknown): Promise<Response> {
  return fetch(`${API_BASE}${path}`, {
    method,
    headers: buildHeaders(body !== undefined),
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
}

/** Makes a GET request to the API. */
async function get<T>(path: string): Promise<T> {
  return handleResponse<T>(await request<T>('GET', path));
}

/** Makes a POST request to the API. */
async function post<T>(path: string, body?: unknown): Promise<T> {
  return handleResponse<T>(await request<T>('POST', path, body));
}

/** Makes a PUT request to the API. */
async function put<T>(path: string, body: unknown): Promise<T> {
  return handleResponse<T>(await request<T>('PUT', path, body));
}

/**
 * Makes a DELETE request to the API.
 */
async function del(path: string): Promise<void> {
  const response = await request<void>('DELETE', path);
  if (!response.ok) {
    const error = await parseErrorResponse(response);
    throw new ApiException(
      response.status,
      error.code ?? 'UNKNOWN_ERROR',
      error.error,
      error.details
    );
  }
}

// ============================================================================
// Zone API
// ============================================================================

/**
 * Lists all zones with optional filtering.
 */
export async function listZones(options?: ListOptions): Promise<ZoneListResponse> {
  return get<ZoneListResponse>(`/zones${buildQueryString(options)}`);
}

/**
 * Gets a zone by ID.
 */
export async function getZone(id: string): Promise<Zone> {
  return get<Zone>(`/zones/${id}`);
}

/**
 * Creates a new zone.
 */
export async function createZone(data: ZoneCreateRequest): Promise<Zone> {
  return post<Zone>('/zones', data);
}

/**
 * Updates an existing zone.
 */
export async function updateZone(id: string, data: ZoneUpdateRequest): Promise<Zone> {
  return put<Zone>(`/zones/${id}`, data);
}

/**
 * Deletes a zone.
 */
export async function deleteZone(id: string): Promise<void> {
  return del(`/zones/${id}`);
}

/**
 * Connects a zone.
 */
export async function connectZone(id: string): Promise<Zone> {
  return post<Zone>(`/zones/${id}/connect`);
}

/**
 * Disconnects a zone.
 */
export async function disconnectZone(id: string): Promise<Zone> {
  return post<Zone>(`/zones/${id}/disconnect`);
}

// ============================================================================
// Qube API
// ============================================================================

/**
 * Lists all qubes with optional filtering.
 */
export async function listQubes(options?: ListOptions): Promise<QubeListResponse> {
  return get<QubeListResponse>(`/qubes${buildQueryString(options)}`);
}

/**
 * Gets a qube by ID.
 */
export async function getQube(id: string): Promise<Qube> {
  return get<Qube>(`/qubes/${id}`);
}

/**
 * Creates a new qube.
 */
export async function createQube(data: QubeCreateRequest): Promise<Operation> {
  return post<Operation>('/qubes', data);
}

/**
 * Updates an existing qube.
 */
export async function updateQube(id: string, data: QubeUpdateRequest): Promise<Qube> {
  return put<Qube>(`/qubes/${id}`, data);
}

/**
 * Deletes a qube.
 */
export async function deleteQube(id: string): Promise<void> {
  return del(`/qubes/${id}`);
}

/**
 * Starts a qube.
 */
export async function startQube(id: string): Promise<Operation> {
  return post<Operation>(`/qubes/${id}/start`);
}

/**
 * Stops a qube.
 */
export async function stopQube(id: string): Promise<Operation> {
  return post<Operation>(`/qubes/${id}/stop`);
}

// ============================================================================
// System API
// ============================================================================

/**
 * Gets health check status.
 */
export async function getHealth(): Promise<HealthResponse> {
  const response = await fetch('/health');
  return handleResponse<HealthResponse>(response);
}

/**
 * Gets system status.
 */
export async function getStatus(): Promise<StatusResponse> {
  return get<StatusResponse>('/status');
}


// ============================================================================
// Orchestration jobs
// ============================================================================

/**
 * Gets a single job — the poll target for the job_id returned by a 202.
 */
export async function getJob(id: string): Promise<Job> {
  return get<Job>(`/jobs/${id}`);
}

/**
 * Lists recent jobs, newest first. Pass qubeId to scope to one qube.
 *
 * This is the audit view: every infrastructure change the console made,
 * including the failures and terraform's own error text.
 */
export async function listJobs(qubeId?: string, limit?: number): Promise<JobListResponse> {
  const params = new URLSearchParams();
  if (qubeId) params.set('qube_id', qubeId);
  if (limit) params.set('limit', String(limit));
  const query = params.toString();
  return get<JobListResponse>(`/jobs${query ? `?${query}` : ''}`);
}


/**
 * Reads live cluster capacity for a zone.
 *
 * Returns 503 when the cluster is unreachable or the zone has no credential,
 * and 501 when no scheduler is configured — both are expected states, so
 * callers should degrade rather than treat them as errors.
 */
export async function listZoneNodes(zoneId: string): Promise<NodeListResponse> {
  return get<NodeListResponse>(`/zones/${zoneId}/nodes`);
}
