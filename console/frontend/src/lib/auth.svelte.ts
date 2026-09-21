/**
 * Qubes Air Console - authentication gate state.
 *
 * The console authenticates the browser with a SHORT-LIVED SESSION COOKIE: the
 * operator pastes the API token once, the API layer exchanges it for an
 * HttpOnly cookie (see api.login), and the token is not kept. Because the
 * cookie is HttpOnly the page cannot ask whether it is present, so the gate is
 * raised by a 401 from the server rather than by a local "no token" check.
 */

class AuthState {
  /**
   * Set when the server rejects a request with 401.
   *
   * The remedy is always "paste the token again" — it was never entered, or it
   * has been rotated.
   */
  rejected = $state(false);

  /** True while the app should show the gate instead of the console. */
  get required(): boolean {
    return this.rejected;
  }

  /** True when a session was attempted and the server refused it. */
  get wasRejected(): boolean {
    return this.rejected;
  }

  /** Called from the API layer on any 401. */
  markRejected(): void {
    this.rejected = true;
  }

  /** Called after the operator exchanges a token, to retry. */
  tokenChanged(): void {
    this.rejected = false;
  }
}

export const auth = new AuthState();
