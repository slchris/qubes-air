/**
 * Qubes Air Console - Global State Store
 *
 * Traditional Svelte writable stores for zones and qubes management.
 */

import { writable, derived, get } from 'svelte/store';
import type { Zone, Qube, QubeStatus, ListOptions } from './types';
import { isTransientStatus } from './types';
import * as api from './api';

// Zone store state
interface ZoneState {
  zones: Zone[];
  loading: boolean;
  error: string | null;
}

// Qube store state
interface QubeState {
  qubes: Qube[];
  loading: boolean;
  error: string | null;
  // The most recent job id per qube, kept so the card can tail that job's
  // terraform output and show its failure reason. The mutating calls return a
  // job_id that was being discarded, which is why a 20-minute apply and a
  // failure were both invisible.
  jobs: Record<string, string>;
}

/**
 * Creates a reactive store for managing zones.
 */
function createZoneStore() {
  const { subscribe, set, update } = writable<ZoneState>({
    zones: [],
    loading: false,
    error: null,
  });

  return {
    subscribe,

    async load(options?: ListOptions): Promise<void> {
      update(s => ({ ...s, loading: true, error: null }));
      try {
        const response = await api.listZones(options);
        update(s => ({ ...s, zones: response.zones ?? [], loading: false }));
      } catch (e) {
        const message = e instanceof Error ? e.message : 'Failed to load zones';
        update(s => ({ ...s, error: message, loading: false }));
      }
    },

    async create(data: Parameters<typeof api.createZone>[0]): Promise<Zone> {
      const zone = await api.createZone(data);
      update(s => ({ ...s, zones: [...s.zones, zone] }));
      return zone;
    },

    async updateZone(id: string, data: Parameters<typeof api.updateZone>[1]): Promise<Zone> {
      const updated = await api.updateZone(id, data);
      update(s => ({
        ...s,
        zones: s.zones.map((z: Zone) => z.id === id ? updated : z),
      }));
      return updated;
    },

    async remove(id: string): Promise<void> {
      await api.deleteZone(id);
      update(s => ({
        ...s,
        zones: s.zones.filter((z: Zone) => z.id !== id),
      }));
    },

    async connect(id: string): Promise<Zone> {
      const updated = await api.connectZone(id);
      update(s => ({
        ...s,
        zones: s.zones.map((z: Zone) => z.id === id ? updated : z),
      }));
      return updated;
    },

    async disconnect(id: string): Promise<Zone> {
      const updated = await api.disconnectZone(id);
      update(s => ({
        ...s,
        zones: s.zones.map((z: Zone) => z.id === id ? updated : z),
      }));
      return updated;
    },

    clearError(): void {
      update(s => ({ ...s, error: null }));
    },

    reset(): void {
      set({ zones: [], loading: false, error: null });
    },
  };
}

/**
 * Creates a reactive store for managing qubes.
 */
function createQubeStore() {
  const { subscribe, set, update } = writable<QubeState>({
    qubes: [],
    loading: false,
    error: null,
    jobs: {},
  });

  /** Records the job a mutating call kicked off, so the card can tail it. */
  function noteJob(qubeId: string, jobId: string | undefined): void {
    if (!jobId) return;
    update(s => ({ ...s, jobs: { ...s.jobs, [qubeId]: jobId } }));
  }

  /**
   * Qubes currently being polled, so overlapping operations do not stack
   * timers on the same qube.
   */
  const watching = new Set<string>();

  /**
   * How often to re-check a qube that has an operation in flight.
   *
   * Terraform applies take minutes (a provision against a real cluster ran
   * about six), so this trades a little staleness for far fewer requests. It
   * is deliberately not sub-second: nothing here completes that fast.
   */
  const POLL_INTERVAL_MS = 3000;

  /** Give up after this long so a stuck job cannot poll forever. */
  const POLL_TIMEOUT_MS = 20 * 60 * 1000;

  /**
   * Polls one qube until it leaves its transient status.
   *
   * The UI cannot simply await the mutating call: those return 202 as soon as
   * the job is queued, and the real outcome lands minutes later on a background
   * worker. Without this the qube would sit at "resuming" until a manual
   * refresh.
   */
  function watch(id: string): void {
    if (watching.has(id)) return;
    watching.add(id);

    const startedAt = Date.now();
    const tick = async (): Promise<void> => {
      try {
        const qube = await api.getQube(id);
        update(s => ({
          ...s,
          qubes: s.qubes.map((q: Qube) => (q.id === id ? qube : q)),
        }));
        if (!isTransientStatus(qube.status)) {
          watching.delete(id);
          return;
        }
      } catch {
        // A transient failure (server restart, network blip) should not end the
        // watch; the timeout below is what bounds it.
      }
      if (Date.now() - startedAt > POLL_TIMEOUT_MS) {
        watching.delete(id);
        return;
      }
      setTimeout(() => void tick(), POLL_INTERVAL_MS);
    };
    setTimeout(() => void tick(), POLL_INTERVAL_MS);
  }

  return {
    subscribe,

    /** Resumes polling for any qube already mid-operation, e.g. after a reload. */
    resumeWatches(): void {
      update(s => {
        s.qubes.filter((q: Qube) => isTransientStatus(q.status)).forEach(q => watch(q.id));
        return s;
      });
    },

    async load(options?: ListOptions): Promise<void> {
      update(s => ({ ...s, loading: true, error: null }));
      try {
        const response = await api.listQubes(options);
        update(s => ({ ...s, qubes: response.qubes ?? [], loading: false }));
      } catch (e) {
        const message = e instanceof Error ? e.message : 'Failed to load qubes';
        update(s => ({ ...s, error: message, loading: false }));
        return;
      }

      // Seed each qube's latest job from the server so its log is viewable even
      // for a qube this browser session did not create — after a reload, or for
      // one provisioned through the API. Without this the card only knew about
      // jobs it kicked off itself, so a succeeded qube showed no log at all.
      // One list call, mapped newest-first (the API returns newest first, so the
      // first id seen per qube is the latest). Session-noted jobs win over this,
      // since they are the operation the user just started.
      try {
        const jobs = await api.listJobs(undefined, 100);
        const latest: Record<string, string> = {};
        for (const j of jobs.jobs ?? []) {
          if (!latest[j.qube_id]) latest[j.qube_id] = j.id;
        }
        update(s => ({ ...s, jobs: { ...latest, ...s.jobs } }));
      } catch {
        // A missing job history is not worth failing the qube list over; the
        // cards simply show no log until the next mutation records one.
      }
    },

    async create(data: Parameters<typeof api.createQube>[0]): Promise<Qube> {
      // Provisioning is asynchronous: the qube arrives in a transient status
      // and settles minutes later, so start watching it immediately.
      const op = await api.createQube(data);
      update(s => ({ ...s, qubes: [...s.qubes, op.qube] }));
      noteJob(op.qube.id, op.job_id);
      watch(op.qube.id);
      return op.qube;
    },

    async updateQube(id: string, data: Parameters<typeof api.updateQube>[1]): Promise<Qube> {
      const updated = await api.updateQube(id, data);
      update(s => ({
        ...s,
        qubes: s.qubes.map((q: Qube) => q.id === id ? updated : q),
      }));
      return updated;
    },

    /**
     * Releases a qube: the compute VM is destroyed, the data disk is kept.
     *
     * The qube deliberately stays in the list. It is NOT gone — it moves to
     * "released" and still owns a data disk, and it must remain in terraform's
     * variable map until that disk is purged. Filtering it out here would have
     * the UI claim a deletion that did not happen.
     */
    async remove(id: string): Promise<void> {
      await api.deleteQube(id);
      update(s => ({
        ...s,
        qubes: s.qubes.map((q: Qube) =>
          q.id === id ? { ...q, status: 'deleting' as QubeStatus } : q),
      }));
      // deleteQube returns no body (DELETE 204), so there is no job_id to note
      // here; the card falls back to the latest job for this qube via
      // GET /jobs?qube_id when it needs one.
      watch(id);
    },

    async start(id: string): Promise<Qube> {
      const op = await api.startQube(id);
      update(s => ({
        ...s,
        qubes: s.qubes.map((q: Qube) => q.id === id ? op.qube : q),
      }));
      noteJob(id, op.job_id);
      watch(id);
      return op.qube;
    },

    async stop(id: string): Promise<Qube> {
      const op = await api.stopQube(id);
      update(s => ({
        ...s,
        qubes: s.qubes.map((q: Qube) => q.id === id ? op.qube : q),
      }));
      noteJob(id, op.job_id);
      watch(id);
      return op.qube;
    },

    clearError(): void {
      update(s => ({ ...s, error: null }));
    },

    reset(): void {
      set({ qubes: [], loading: false, error: null, jobs: {} });
    },
  };
}

// Export singleton stores
export const zoneStore = createZoneStore();
export const qubeStore = createQubeStore();

// Derived stores for convenience
export const zones = derived(zoneStore, $store => $store.zones);
export const zonesLoading = derived(zoneStore, $store => $store.loading);
export const zonesError = derived(zoneStore, $store => $store.error);

export const qubes = derived(qubeStore, $store => $store.qubes);
export const qubesLoading = derived(qubeStore, $store => $store.loading);
export const qubesError = derived(qubeStore, $store => $store.error);

// Helper to get connected zones
export const connectedZones = derived(zoneStore, $store =>
  $store.zones.filter((z: Zone) => z.status === 'connected')
);
