import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import QubeFormDialog from './QubeFormDialog.svelte'
import * as api from '../lib/api'
import { qubeStore } from '../lib/stores'
import type { NodeInfo, Qube, Zone } from '../lib/types'

// Only the capacity lookup is replaced. Create and update go through the real
// API layer, so a refusal reaches the dialog the way the console sends it.
vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, getZoneCapacity: vi.fn() }
})

const getZoneCapacity = vi.mocked(api.getZoneCapacity)
const GiB = 1024 ** 3

const zone: Zone = {
  id: 'z1',
  name: 'lab',
  type: 'proxmox',
  status: 'connected',
  config: { endpoint: 'https://pve.example.test:8006' },
  created_at: '2026-09-26T00:00:00Z',
  updated_at: '2026-09-26T00:00:00Z',
}

function node(name: string, freeGiB: number, online = true): NodeInfo {
  return {
    name, online, max_cpu: 4, cpu_usage: 0.1,
    mem_total_bytes: 32 * GiB, mem_used_bytes: (32 - freeGiB) * GiB, mem_free_bytes: freeGiB * GiB,
  }
}

function qubeFixture(overrides: Partial<Qube> = {}): Qube {
  return {
    purge_requested: false,
    id: 'q1',
    name: 'qube-one',
    type: 'dev',
    status: 'stopped',
    zone_id: 'z1',
    spec: { vcpu: 2, memory: 2048, disk: 20, data_disk_gb: 30, node: 'pve-1' },
    created_at: '2026-09-21T00:00:00Z',
    updated_at: '2026-09-21T00:00:00Z',
    ...overrides,
  }
}

function renderCreate(connectedZones: Zone[] = [zone], onclose = vi.fn()) {
  return render(QubeFormDialog, {
    props: { mode: 'create', connectedZones, zones: connectedZones, onclose },
  })
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  getZoneCapacity.mockReset()
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('QubeFormDialog bounds', () => {
  // The API refuses a spec outside qube_spec.* (internal/service/specbounds.go);
  // config_test.go pins the same four numbers on the Go side. If either side
  // moves alone, the form offers what the API refuses, or blocks what it takes.
  it('limits the spec inputs to the API defaults', () => {
    getZoneCapacity.mockResolvedValue({ kind: 'unknown' })
    renderCreate()

    expect(screen.getByLabelText(/^vcpu$/i)).toHaveAttribute('min', '1')
    expect(screen.getByLabelText(/^vcpu$/i)).toHaveAttribute('max', '32')
    expect(screen.getByLabelText(/^memory \(mb\)$/i)).toHaveAttribute('min', '512')
    expect(screen.getByLabelText(/^disk \(gb\)$/i)).toHaveAttribute('min', '10')
    expect(screen.getByLabelText(/^data disk \(gb\)$/i)).toHaveAttribute('min', '1')
  })
})

describe('QubeFormDialog create', () => {
  it('shows the API reason for a refused spec, not the HTTP status text', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({
      error: 'Bad Request',
      message: 'invalid qube spec: spec.disk 200000 GB is above the maximum 16384 GB (qube_spec.max_disk_gb)',
    }), { status: 400, headers: { 'Content-Type': 'application/json' } }))
    const onclose = vi.fn()
    renderCreate([], onclose)

    await userEvent.type(screen.getByLabelText(/^name$/i), 'big-disk')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))

    expect(await screen.findByText(/above the maximum 16384 GB/)).toBeInTheDocument()
    expect(screen.queryByText(/^bad request$/i)).not.toBeInTheDocument()
    expect(onclose).not.toHaveBeenCalled()
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/qubes')
    expect(init.method).toBe('POST')
  })

  it('offers only the nodes that fit and says which one automatic would pick', async () => {
    getZoneCapacity.mockResolvedValue({
      kind: 'node_pool',
      nodes: [node('pve-1', 10), node('pve-2', 20), node('pve-3', 28, false), node('pve-4', 5.5)],
    })
    const create = vi.spyOn(qubeStore, 'create').mockResolvedValue(qubeFixture())
    const onclose = vi.fn()
    renderCreate([zone], onclose)

    // The picker renders at once and fills in when the capacity read lands.
    expect(await screen.findByRole('option', { name: /automatic — would pick pve-2/i })).toBeInTheDocument()
    expect(getZoneCapacity).toHaveBeenCalledWith('z1')
    const picker = screen.getByLabelText(/^node$/i) as HTMLSelectElement
    // Offline and too-small nodes are listed, not selectable: 5.5 GiB free minus
    // the 15% reserve of 32 GiB leaves less than the 2048 MB default.
    expect(within(picker).getByRole('option', { name: /pve-3.*offline/i })).toBeDisabled()
    expect(within(picker).getByRole('option', { name: /pve-4.*insufficient/i })).toBeDisabled()
    expect(within(picker).getByRole('option', { name: /pve-1/i })).toBeEnabled()

    await userEvent.type(screen.getByLabelText(/^name$/i), 'placed')
    await userEvent.selectOptions(picker, 'pve-1')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))

    expect(create).toHaveBeenCalledWith({
      name: 'placed',
      type: 'work',
      zone_id: 'z1',
      spec: { vcpu: 2, memory: 2048, disk: 20, data_disk_gb: 20, node: 'pve-1' },
    })
    expect(onclose).toHaveBeenCalledOnce()
  })

  it('falls back to a free-text node when capacity cannot be read', async () => {
    getZoneCapacity.mockRejectedValue(new api.ApiException(503, 'UNAVAILABLE', 'cluster unreachable'))
    const create = vi.spyOn(qubeStore, 'create').mockResolvedValue(qubeFixture())
    renderCreate()

    expect(await screen.findByText(/capacity unavailable \(cluster unreachable\)/i)).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText(/^name$/i), 'typed-node')
    await userEvent.type(screen.getByPlaceholderText('zone default'), 'pve-9')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))

    expect(create).toHaveBeenCalledWith(expect.objectContaining({
      spec: expect.objectContaining({ node: 'pve-9' }),
    }))
  })

  it('offers no node field on an elastic provider', async () => {
    getZoneCapacity.mockResolvedValue({
      kind: 'quota',
      quota: { instances_used: 3, vcpu_used: 6, vcpu_limit: 24, memory_mb_used: 6144 },
    })
    renderCreate()

    expect(await screen.findByText(/handled by the provider/i)).toHaveTextContent('Using 6 of 24 vCPU across 3 instances.')
    expect(screen.queryByLabelText(/^node$/i)).not.toBeInTheDocument()
  })

  it('sends no zone and reads no capacity when none is connected', async () => {
    const create = vi.spyOn(qubeStore, 'create').mockResolvedValue(qubeFixture())
    renderCreate([])

    await userEvent.type(screen.getByLabelText(/^name$/i), 'zoneless')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))

    expect(getZoneCapacity).not.toHaveBeenCalled()
    expect(create).toHaveBeenCalledWith(expect.not.objectContaining({ zone_id: expect.anything() }))
  })
})

describe('QubeFormDialog edit', () => {
  it('starts from the qube and reads no capacity', () => {
    render(QubeFormDialog, {
      props: { mode: 'edit', qube: qubeFixture(), connectedZones: [zone], zones: [zone], onclose: vi.fn() },
    })

    expect(screen.getByRole('dialog', { name: /edit qube/i })).toBeInTheDocument()
    expect(screen.getByLabelText(/^name$/i)).toHaveValue('qube-one')
    expect(screen.getByLabelText(/^data disk \(gb\)$/i)).toHaveValue(30)
    expect(screen.queryByLabelText(/^node$/i)).not.toBeInTheDocument()
    expect(getZoneCapacity).not.toHaveBeenCalled()
  })

  it('keeps what the operator typed when the qube row refreshes underneath', async () => {
    const onclose = vi.fn()
    const { rerender } = render(QubeFormDialog, {
      props: { mode: 'edit', qube: qubeFixture(), connectedZones: [zone], zones: [zone], onclose },
    })
    await userEvent.clear(screen.getByLabelText(/^name$/i))
    await userEvent.type(screen.getByLabelText(/^name$/i), 'being-edited')

    await rerender({ qube: qubeFixture({ name: 'polled-name', status: 'running' }) })

    expect(screen.getByLabelText(/^name$/i)).toHaveValue('being-edited')
  })

  it('updates the qube it was opened for and keeps its pinned node', async () => {
    const update = vi.spyOn(qubeStore, 'updateQube').mockResolvedValue(qubeFixture())
    const onclose = vi.fn()
    render(QubeFormDialog, {
      props: { mode: 'edit', qube: qubeFixture(), connectedZones: [zone], zones: [zone], onclose },
    })

    await userEvent.clear(screen.getByLabelText(/^data disk \(gb\)$/i))
    await userEvent.type(screen.getByLabelText(/^data disk \(gb\)$/i), '40')
    await userEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(update).toHaveBeenCalledWith('q1', {
      name: 'qube-one',
      spec: { vcpu: 2, memory: 2048, disk: 20, data_disk_gb: 40, node: 'pve-1' },
    })
    expect(onclose).toHaveBeenCalledOnce()
  })
})
