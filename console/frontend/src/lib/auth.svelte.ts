/**
 * Qubes Air Console - authentication gate state.
 *
 * The console authenticates with a single static bearer token that the operator
 * reads out of the console qube and pastes in once (see the console's Settings
 * view, and salt/qubesair/README.md "Opening the console"). There is no login
 * endpoint and no session — so "not authenticated" has to be a state the UI
 * knows about, not merely an error each view renders on its own.
 *
 * Without this, a console with no token loaded every view, fired every request,
 * and painted a screen of failures. Nothing said which one thing to do about
 * it, and the natural reading was that the console was broken.
 */

import { getApiToken } from './token';

/** True when no token is stored in this browser. */
function missingToken(): boolean {
  return !getApiToken();
}

class AuthState {
  /**
   * Set when the server rejects a request with 401.
   *
   * Tracked separately from "no token stored", because the two are different
   * situations with the same remedy: a token that was never entered, versus one
   * that is present but wrong (rotated, mistyped, or copied from another
   * deployment). The gate reports which one it is.
   */
  rejected = $state(false);

  /** Bumped whenever the stored token changes, to re-evaluate `required`. */
  private generation = $state(0);

  /** True while the app should show the gate instead of the console. */
  get required(): boolean {
    // Reading `generation` registers the dependency, so saving a token
    // re-evaluates this without a page reload.
    void this.generation;
    return this.rejected || missingToken();
  }

  /** True when a token exists but the server refused it. */
  get wasRejected(): boolean {
    void this.generation;
    return this.rejected && !missingToken();
  }

  /** Called from the API layer on any 401. */
  markRejected(): void {
    this.rejected = true;
  }

  /** Called after the operator saves a token, to retry. */
  tokenChanged(): void {
    this.rejected = false;
    this.generation += 1;
  }
}

export const auth = new AuthState();
