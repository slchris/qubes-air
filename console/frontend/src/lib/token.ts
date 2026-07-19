/**
 * Qubes Air Console - API token storage.
 *
 * Its own module so that the API layer and the auth gate can both read it
 * without importing each other. api.ts needs the gate (to raise it on 401) and
 * the gate needs the token (to know whether one exists); routing both through
 * this leaf module keeps the imports static and acyclic, and keeps the storage
 * key in exactly one place.
 */

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

/** Writes the token, or clears it when given an empty value. */
export function writeApiToken(token: string): void {
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
