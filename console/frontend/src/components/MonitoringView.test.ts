import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import MonitoringView from './MonitoringView.svelte'
import * as api from '../lib/api'
import type { NodeInfo, Zone, ZoneCapacity } from '../lib/types'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, apiFetch: vi.fn(), listZones: vi.fn(), getZoneCapacity: vi.fn() }
})

const apiFetch = vi.mocked(api.apiFetch)
const listZones = vi.mocked(api.listZones)
const getZoneCapacity = vi.mocked(api.getZoneCapacity)

// The view asks the console's own /monitoring endpoint for process metrics and
// then asks every zone for real cluster capacity. Those are two independent
// failure domains, so the fixtures below always answer both.
function monitoringResponse(body: Record<string, unknown>): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response
}

function zoneFixture(overrides: Partial<Zone> = {}): Zone {
  const base: Zone = {
    id: 'z1',
    name: 'lab',
    type: 'proxmox',
    status: 'connected',
    config: { endpoint: 'https://pve.lab:8006' },
    created_at: '2026-09-22T00:00:00Z',
    updated_at: '2026-09-22T00:00:00Z',
  }
  return { ...base, ...overrides }
}

function nodeFixture(overrides: Partial<NodeInfo> = {}): NodeInfo {
  return {
    name: 'pve-1',
    online: true,
    max_cpu: 8,
    cpu_usage: 0.5,
    mem_used_bytes: 4 * 1024 ** 3,
    mem_total_bytes: 16 * 1024 ** 3,
    mem_free_bytes: 7 * 1024 ** 3,
    ...overrides,
  }
}

function capacityFixture(nodes: NodeInfo[]): ZoneCapacity {
  return { kind: 'node_pool', nodes }
}

beforeEach(() => {
  apiFetch.mockReset()
  listZones.mockReset()
  getZoneCapacity.mockReset()
  apiFetch.mockResolvedValue(monitoringResponse({ metrics: {}, alerts: [] }))
  listZones.mockResolvedValue({ zones: [], total: 0 } as never)
})

describe('MonitoringView cluster capacity', () => {
  it('renders the per-node numbers that describe the fleet, not the process', async () => {
    listZones.mockResolvedValue({ zones: [zoneFixture()], total: 1 } as never)
    getZoneCapacity.mockResolvedValue(capacityFixture([nodeFixture()]))

    render(MonitoringView)

    expect(await screen.findByText('pve-1')).toBeInTheDocument()
    // cpu_usage is a 0..1 fraction on the wire and a percentage in the UI, and the
    // memory column is derived from the used/total bytes rather than a ratio the
    // node reports. The two must not be confused: 50% vs 25% here.
    expect(screen.getByText(/50\.0%/)).toBeInTheDocument()
    expect(screen.getByText('25.0%')).toBeInTheDocument()
    expect(screen.getByText(/of 8c/)).toBeInTheDocument()
    expect(screen.getByText(/7\.0 GiB/)).toBeInTheDocument()
  })

  it('names why a zone reports no capacity instead of showing a bare HTTP reason', async () => {
    // Both branches matter and they are different sentences: a zone the operator
    // has not connected yet is not the same fault as one whose cluster is
    // unreachable, and "Service Unavailable" tells them neither.
    listZones.mockResolvedValue({
      zones: [
        zoneFixture({ id: 'z1', name: 'offline-zone', status: 'disconnected' }),
        zoneFixture({ id: 'z2', name: 'broken-zone', status: 'connected' }),
      ],
      total: 2,
    } as never)
    getZoneCapacity.mockRejectedValue(new Error('Service Unavailable'))

    render(MonitoringView)

    expect(await screen.findByText('offline-zone')).toBeInTheDocument()
    expect(screen.getByText('not connected')).toBeInTheDocument()
    expect(screen.getByText(/cluster unreachable/i)).toBeInTheDocument()
  })

  it('says so when no zone is configured rather than rendering an empty section', async () => {
    render(MonitoringView)

    expect(await screen.findByText(/no zones configured/i)).toBeInTheDocument()
  })
})

describe('MonitoringView placeholder honesty', () => {
  it('keeps the backend note with the numbers it describes', async () => {
    // The regression this guards: process metrics rendered without the banner
    // were read as fleet measurements ("Disk Usage 0%" is not a measurement).
    apiFetch.mockResolvedValue(
      monitoringResponse({
        metrics: { cpuUsage: 12.3456, diskUsage: 0 },
        alerts: [],
        placeholder: true,
        note: 'These describe the console process, not the managed qubes.',
      }),
    )

    render(MonitoringView)

    expect(await screen.findByText(/placeholder metrics/i)).toBeInTheDocument()
    expect(screen.getByText(/not the managed qubes/i)).toBeInTheDocument()
    expect(screen.getByText('12.35%')).toBeInTheDocument()
  })

  it('shows an active alert with its severity and source', async () => {
    apiFetch.mockResolvedValue(
      monitoringResponse({
        metrics: {},
        alerts: [
          {
            id: 'a1',
            severity: 'critical',
            message: 'zone lab is unreachable',
            source: 'agent-probe',
            timestamp: '2026-09-22T00:00:00Z',
            acknowledged: false,
          },
        ],
        placeholder: false,
      }),
    )

    render(MonitoringView)

    expect(await screen.findByText('zone lab is unreachable')).toBeInTheDocument()
    expect(screen.getByText('CRITICAL')).toBeInTheDocument()
    expect(screen.getByText('Source: agent-probe')).toBeInTheDocument()
  })
})

describe('MonitoringView failure handling', () => {
  it('reports a failed metrics load and recovers on retry', async () => {
    // A silently empty view would read as "nothing to report"; the failure has to
    // be stated and the retry has to actually re-ask.
    apiFetch.mockResolvedValueOnce({
      ok: false,
      status: 503,
      json: async () => ({}),
    } as unknown as Response)
    apiFetch.mockResolvedValue(
      monitoringResponse({ metrics: { cpuUsage: 1 }, alerts: [], placeholder: false }),
    )

    render(MonitoringView)

    expect(await screen.findByText(/error: failed to load monitoring data/i)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))

    expect(await screen.findByText('Console process')).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledTimes(2)
  })
})
