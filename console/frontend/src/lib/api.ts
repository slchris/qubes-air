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
 * Makes a GET request to the API.
 */
async function get<T>(path: string): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`);
  return handleResponse<T>(response);
}

/**
 * Makes a POST request to the API.
 */
async function post<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  });
  return handleResponse<T>(response);
}

/**
 * Makes a PUT request to the API.
 */
async function put<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return handleResponse<T>(response);
}

/**
 * Makes a DELETE request to the API.
 */
async function del(path: string): Promise<void> {
  const response = await fetch(`${API_BASE}${path}`, { method: 'DELETE' });
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
export async function createQube(data: QubeCreateRequest): Promise<Qube> {
  return post<Qube>('/qubes', data);
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
export async function startQube(id: string): Promise<Qube> {
  return post<Qube>(`/qubes/${id}/start`);
}

/**
 * Stops a qube.
 */
export async function stopQube(id: string): Promise<Qube> {
  return post<Qube>(`/qubes/${id}/stop`);
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
