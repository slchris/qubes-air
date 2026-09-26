import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import ZonesView from './ZonesView.svelte'
import * as api from '../lib/api'
import { zoneStore } from '../lib/stores'
import type { Zone } from '../lib/types'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return {
    ...actual,
    apiFetch: vi.fn(),
    listZones: vi.fn(),
    createZone: vi.fn(),
  }
})

const apiFetch = vi.mocked(api.apiFetch)
const listZones = vi.mocked(api.listZones)
const createZone = vi.mocked(api.createZone)

function zoneFixture(overrides: Partial<Zone> = {}): Zone {
  return {
    id: 'z1',
    name: 'infra',
    type: 'proxmox',
    status: 'disconnected',
    config: { endpoint: 'https://pve.example.com/', proxmox: { credential_id: 'c1', template_vm_id: 901 } },
    created_at: '2026-09-26T00:00:00Z',
    updated_at: '2026-09-26T00:00:00Z',
    ...overrides,
  }
}

function jsonResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: async () => body } as unknown as Response
}

beforeEach(() => {
  zoneStore.reset()
  listZones.mockResolvedValue({ zones: [], total: 0 } as never)
  createZone.mockReset()
  apiFetch.mockResolvedValue(jsonResponse({
    credentials: [{ id: 'c1', name: 'pve-root', type: 'proxmox' }],
  }))
})

afterEach(() => {
  vi.restoreAllMocks()
})

async function openAddZone(): Promise<HTMLElement> {
  render(ZonesView)
  await userEvent.click(await screen.findByRole('button', { name: /\+ add zone/i }))
  return screen.getByRole('dialog', { name: /add zone/i })
}

function providerOption(dialog: HTMLElement, value: string): HTMLOptionElement {
  const select = within(dialog).getByLabelText(/^provider$/i) as HTMLSelectElement
  const option = Array.from(select.options).find(o => o.value === value)
  if (!option) throw new Error(`no ${value} option`)
  return option
}

describe('ZonesView provider picker', () => {
  it('starts on Proxmox, the one provider with an adapter', async () => {
    const dialog = await openAddZone()

    const select = within(dialog).getByLabelText(/^provider$/i) as HTMLSelectElement
    expect(select.value).toBe('proxmox')
    expect(providerOption(dialog, 'proxmox').disabled).toBe(false)
    expect(providerOption(dialog, 'proxmox').textContent).toBe('Proxmox')
  })

  it('lists GCP, AWS and Azure as not implemented and not selectable', async () => {
    // AGENTS.md §1: a provider that has not passed acceptance must not look
    // available. The backend refuses these types with 422; the UI must not
    // offer a choice that can only end in that refusal.
    const dialog = await openAddZone()

    for (const [value, label] of [['gcp', 'Google Cloud'], ['aws', 'AWS'], ['azure', 'Azure']]) {
      const option = providerOption(dialog, value)
      expect(option.disabled).toBe(true)
      expect(option.textContent).toBe(`${label} (not implemented)`)
    }
    expect(within(dialog).getByText(/google cloud, aws, azure: not implemented/i)).toBeInTheDocument()
  })

  it('cannot be switched to an unimplemented provider', async () => {
    // Negative path: a disabled option cannot be chosen, so the selection stays
    // on Proxmox and no GCP-only form (project, bootstrap bucket) is reached.
    const dialog = await openAddZone()
    const select = within(dialog).getByLabelText(/^provider$/i) as HTMLSelectElement

    await userEvent.selectOptions(select, 'gcp')
    expect(select.value).toBe('proxmox')
    expect(within(dialog).queryByLabelText(/^project$/i)).not.toBeInTheDocument()
    expect(within(dialog).queryByLabelText(/bootstrap bucket/i)).not.toBeInTheDocument()
    // The removed Terraform deployment path must not be described as live.
    expect(dialog.textContent).not.toMatch(/terraform/i)
  })
})

describe('ZonesView create', () => {
  async function fillProxmoxZone(dialog: HTMLElement): Promise<void> {
    await userEvent.type(within(dialog).getByLabelText(/^name$/i), 'infra')
    await userEvent.selectOptions(await within(dialog).findByLabelText(/^credential$/i), 'c1')
    await userEvent.type(within(dialog).getByLabelText(/^api endpoint$/i), 'https://pve.example.com/')
    await userEvent.type(within(dialog).getByLabelText(/^template vmid$/i), '901')
  }

  it('submits a proxmox zone without the operator touching the picker', async () => {
    createZone.mockResolvedValue(zoneFixture())
    const dialog = await openAddZone()

    await fillProxmoxZone(dialog)
    await userEvent.click(within(dialog).getByRole('button', { name: /create zone/i }))

    expect(createZone).toHaveBeenCalledTimes(1)
    const req = createZone.mock.calls[0][0]
    expect(req.type).toBe('proxmox')
    expect(req.config.proxmox).toMatchObject({ credential_id: 'c1', template_vm_id: 901 })
    expect(req.config.gcp).toBeUndefined()
    expect(screen.queryByRole('dialog', { name: /add zone/i })).not.toBeInTheDocument()
  })

  it('keeps the dialog open and shows the backend refusal', async () => {
    // The backend is the authority on which providers exist; if it refuses
    // what the UI offered, the operator must see its reason, not a generic one.
    createZone.mockRejectedValue(new api.ApiException(422, 'UNKNOWN_ERROR',
      'provider not implemented: zone type "proxmox" has no provider adapter registered in this console'))
    const dialog = await openAddZone()

    await fillProxmoxZone(dialog)
    await userEvent.click(within(dialog).getByRole('button', { name: /create zone/i }))

    expect(await within(dialog).findByText(/provider not implemented/i)).toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: /add zone/i })).toBeInTheDocument()
  })
})

describe('ZonesView zone cards', () => {
  it('marks a stored zone of an unimplemented provider', async () => {
    // Such a zone predates the backend refusing its type. It must stay listed
    // (the API can still delete it) and must not read as usable.
    listZones.mockResolvedValue({
      zones: [zoneFixture(), zoneFixture({ id: 'z2', name: 'old-gcp', type: 'gcp', config: { endpoint: '' } })],
      total: 2,
    } as never)

    render(ZonesView)

    const gcpCard = (await screen.findByText('old-gcp')).closest('article') as HTMLElement
    expect(within(gcpCard).getByText('not implemented')).toBeInTheDocument()
    const pveCard = screen.getByText('infra').closest('article') as HTMLElement
    expect(within(pveCard).queryByText('not implemented')).not.toBeInTheDocument()
  })
})
