/**
 * Qubes Air Console - Global State Store
 *
 * Traditional Svelte writable stores for zones and qubes management.
 */

import { writable, derived, get } from 'svelte/store';
import type { Zone, Qube, ListOptions } from './types';
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
  });

  return {
    subscribe,

    async load(options?: ListOptions): Promise<void> {
      update(s => ({ ...s, loading: true, error: null }));
      try {
        const response = await api.listQubes(options);
        update(s => ({ ...s, qubes: response.qubes ?? [], loading: false }));
      } catch (e) {
        const message = e instanceof Error ? e.message : 'Failed to load qubes';
        update(s => ({ ...s, error: message, loading: false }));
      }
    },

    async create(data: Parameters<typeof api.createQube>[0]): Promise<Qube> {
      const qube = await api.createQube(data);
      update(s => ({ ...s, qubes: [...s.qubes, qube] }));
      return qube;
    },

    async updateQube(id: string, data: Parameters<typeof api.updateQube>[1]): Promise<Qube> {
      const updated = await api.updateQube(id, data);
      update(s => ({
        ...s,
        qubes: s.qubes.map((q: Qube) => q.id === id ? updated : q),
      }));
      return updated;
    },

    async remove(id: string): Promise<void> {
      await api.deleteQube(id);
      update(s => ({
        ...s,
        qubes: s.qubes.filter((q: Qube) => q.id !== id),
      }));
    },

    async start(id: string): Promise<Qube> {
      const updated = await api.startQube(id);
      update(s => ({
        ...s,
        qubes: s.qubes.map((q: Qube) => q.id === id ? updated : q),
      }));
      return updated;
    },

    async stop(id: string): Promise<Qube> {
      const updated = await api.stopQube(id);
      update(s => ({
        ...s,
        qubes: s.qubes.map((q: Qube) => q.id === id ? updated : q),
      }));
      return updated;
    },

    clearError(): void {
      update(s => ({ ...s, error: null }));
    },

    reset(): void {
      set({ qubes: [], loading: false, error: null });
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
