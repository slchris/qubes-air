/**
 * Qubes Air Console - authentication gate state.
 *
 * The console authenticates the browser with a SHORT-LIVED SESSION COOKIE: the
 * operator pastes the API token once, the API layer exchanges it for an
 * HttpOnly cookie (see api.login), and the token is not kept. Because the
 * cookie is HttpOnly the page cannot ask whether it is present, so the gate is
 * raised by a 401 from the server rather than by a local "no token" check.
 *
 * The same holds for the session's zone restriction: the page learns it from
 * the server (the login answer, or GET /session after a reload) and uses it
 * only to decide what to show. The server enforces the restriction on every
 * request whatever the page shows.
 */

/**
 * Views backed only by fleet-wide endpoints. The server refuses all of them to
 * a zone-scoped credential (403): credentials, billing and monitoring are
 * fleet prefixes, and the job history is the unqualified job listing. Settings
 * is fleet-only too, but its view also holds the sign-in and sign-out controls,
 * so it stays reachable and shows only those (see SettingsView).
 */
export const FLEET_ONLY_VIEWS: ReadonlySet<string> = new Set(['jobs', 'credentials', 'billing', 'monitoring']);

class AuthState {
  /**
   * Set when the server rejects a request with 401.
   *
   * The remedy is always "paste the token again" — it was never entered, or it
   * has been rotated.
   */
  rejected = $state(false);

  /**
   * False until the server has said who this browser is (a session scope) or
   * refused it (401). The shell waits for it: rendering first would briefly
   * offer a zone-scoped session views the server is about to refuse.
   */
  initialized = $state(false);

  /** Zone IDs the current credential is limited to; empty means fleet-wide. */
  zones = $state<string[]>([]);

  /** True when the current credential may address only some zones. */
  get zoneScoped(): boolean {
    return this.zones.length > 0;
  }

  /** True while the app should show the gate instead of the console. */
  get required(): boolean {
    return this.rejected;
  }

  /** True when a session was attempted and the server refused it. */
  get wasRejected(): boolean {
    return this.rejected;
  }

  /** Whether the navigation should offer a view to this credential. */
  canOpen(view: string): boolean {
    return !(this.zoneScoped && FLEET_ONLY_VIEWS.has(view));
  }

  /** Called from the API layer on any 401. */
  markRejected(): void {
    this.rejected = true;
    this.initialized = true;
  }

  /**
   * Applies the scope the server reported for the current session and lowers
   * the gate. The zones carry no authority; they only shape the navigation.
   */
  setSession(zones: string[] | null | undefined): void {
    this.zones = Array.isArray(zones) ? [...zones] : [];
    this.rejected = false;
    this.initialized = true;
  }
}

export const auth = new AuthState();
