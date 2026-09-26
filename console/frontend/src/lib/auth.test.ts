import { beforeEach, describe, expect, it } from 'vitest'

import { FLEET_ONLY_VIEWS, auth } from './auth.svelte'

// The zone list only shapes navigation; these pin how it is recorded so a
// zone-scoped session is never shown as fleet-wide by accident.

beforeEach(() => {
  auth.rejected = false
  auth.initialized = false
  auth.zones = []
})

describe('auth session scope', () => {
  it('starts uninitialised so the shell waits for the server', () => {
    expect(auth.initialized).toBe(false)
    expect(auth.required).toBe(false)
  })

  it('records the zones the server reported and lowers the gate', () => {
    auth.markRejected()
    auth.setSession(['z1', 'z2'])

    expect(auth.initialized).toBe(true)
    expect(auth.required).toBe(false)
    expect(auth.zones).toEqual(['z1', 'z2'])
    expect(auth.zoneScoped).toBe(true)
  })

  const unrestricted: { name: string; zones: string[] | null | undefined }[] = [
    { name: 'null', zones: null },
    { name: 'missing', zones: undefined },
    { name: 'empty', zones: [] },
  ]
  it.each(unrestricted)('treats $name zones as fleet-wide', ({ zones }) => {
    auth.setSession(zones)

    expect(auth.zoneScoped).toBe(false)
    expect(auth.initialized).toBe(true)
  })

  it('copies the zone list so the caller cannot change it afterwards', () => {
    const zones = ['z1']
    auth.setSession(zones)
    zones.push('z2')

    expect(auth.zones).toEqual(['z1'])
  })

  it('counts a 401 as initialised so the gate, not the spinner, is shown', () => {
    auth.markRejected()

    expect(auth.initialized).toBe(true)
    expect(auth.required).toBe(true)
  })
})

describe('auth.canOpen', () => {
  it('offers every view to a fleet-wide session', () => {
    auth.setSession([])
    for (const view of ['dashboard', 'qubes', 'zones', 'settings', ...FLEET_ONLY_VIEWS]) {
      expect(auth.canOpen(view)).toBe(true)
    }
  })

  it('withholds the fleet-only views from a zone-scoped session', () => {
    auth.setSession(['z1'])
    for (const view of FLEET_ONLY_VIEWS) {
      expect(auth.canOpen(view)).toBe(false)
    }
    // Settings stays reachable: it holds the sign-out control.
    for (const view of ['dashboard', 'qubes', 'zones', 'settings']) {
      expect(auth.canOpen(view)).toBe(true)
    }
  })

  it('lists exactly the views backed by fleet-only endpoints', () => {
    expect([...FLEET_ONLY_VIEWS].sort()).toEqual(['billing', 'credentials', 'jobs', 'monitoring'])
  })
})
