import { describe, expect, it } from 'vitest'

import {
  DEFAULT_ZONE_PROVIDER, ZONE_PROVIDERS,
  isProviderImplemented, isUnimplementedProvider, providerOptionLabel,
} from './providers'

describe('provider availability', () => {
  it('defaults to a provider the console can provision into', () => {
    // Every form starts here; an unimplemented default is how a credential
    // nothing could use got created by an operator who never opened the picker.
    expect(isProviderImplemented(DEFAULT_ZONE_PROVIDER)).toBe(true)
  })

  it('reports Proxmox as the only implemented provider', () => {
    // Mirrors the backend registry (cmd/server/main.go registers Proxmox only).
    expect(ZONE_PROVIDERS.filter(p => p.implemented).map(p => p.value)).toEqual(['proxmox'])
    for (const t of ['gcp', 'aws', 'azure']) {
      expect(isProviderImplemented(t)).toBe(false)
      expect(isUnimplementedProvider(t)).toBe(true)
    }
  })

  it('treats non-provider and unknown types as neither', () => {
    for (const t of ['ssh', 'api_key', 'other', '', 'kubevirt']) {
      expect(isProviderImplemented(t)).toBe(false)
      expect(isUnimplementedProvider(t)).toBe(false)
    }
  })

  it('labels only unimplemented options', () => {
    const byValue = (v: string) => ZONE_PROVIDERS.find(p => p.value === v)!
    expect(providerOptionLabel(byValue('proxmox'))).toBe('Proxmox')
    expect(providerOptionLabel(byValue('gcp'))).toBe('Google Cloud (not implemented)')
  })
})
