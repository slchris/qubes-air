import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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

// The view asks the console host's /monitoring endpoint for host metrics, the
// /monitoring/qubes endpoint for per-qube provider metrics, and every zone for
// cluster capacity. Those are independent failure domains, so the fixtures
// below always answer all of them.
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

afterEach(() => {
  vi.useRealTimers()
})

function failedResponse(): Response {
  return { ok: false, status: 503, json: async () => ({}) } as unknown as Response
}

// Answers /monitoring/qubes with items and every other path with body.
function routeRuntime(items: unknown[], body: Record<string, unknown> = { metrics: {}, alerts: [] }) {
  apiFetch.mockImplementation(async (path) => path === '/monitoring/qubes'
    ? monitoringResponse({ items })
    : monitoringResponse(body))
}

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

describe('MonitoringView runtime telemetry', () => {
  it('renders live measurements and explicit unavailable reasons', async () => {
    routeRuntime([
      {
        qube_id: 'q1', qube_name: 'work-vm', zone_id: 'z1', state: 'running',
        metrics: {
          captured_at: new Date(Date.now() - 30_000).toISOString(), source: 'proxmox-qemu-status-current',
          cpu_fraction: 0.25, memory_used_bytes: 512 * 1024 ** 2, memory_max_bytes: 2 * 1024 ** 3,
          disk_read_bytes: 1024, disk_write_bytes: 2048, network_in_bytes: 4096, network_out_bytes: 8192,
        },
      },
      { qube_id: 'q2', qube_name: 'suspended-vm', zone_id: 'z1', state: 'suspended', reason: 'qube_not_running' },
      { qube_id: 'q3', qube_name: 'slow-vm', zone_id: 'z1', state: 'running', reason: 'provider_metrics_timeout' },
    ])

    render(MonitoringView)

    expect(await screen.findByText('work-vm')).toBeInTheDocument()
    expect(screen.getByText(/CPU 25\.00%/)).toBeInTheDocument()
    expect(screen.getByText(/512 MB \/ 2 GB/)).toBeInTheDocument()
    expect(screen.getByText(/Disk I\/O since start/)).toBeInTheDocument()
    expect(screen.getByText(/proxmox-qemu-status-current/)).toBeInTheDocument()
    expect(screen.getByText(/not running; runtime metrics are not available/i)).toBeInTheDocument()
    expect(screen.getByText(/did not answer in time/i)).toBeInTheDocument()
    expect(screen.queryByText(/stale measurement/i)).not.toBeInTheDocument()
  })

  it('shows a value the provider omitted as missing, not zero', async () => {
    routeRuntime([{
      qube_id: 'q1', qube_name: 'partial-vm', zone_id: 'z1', state: 'running',
      metrics: { captured_at: new Date().toISOString(), source: 'test', cpu_fraction: 0.5 },
    }])

    render(MonitoringView)

    expect(await screen.findByText('partial-vm')).toBeInTheDocument()
    expect(screen.getByText('Memory —')).toBeInTheDocument()
    expect(screen.getByText('Network since start —')).toBeInTheDocument()
    expect(screen.queryByText(/0 B/)).not.toBeInTheDocument()
  })

  it('labels provider samples older than the freshness window as stale', async () => {
    routeRuntime([{
      qube_id: 'q1', qube_name: 'old-sample', zone_id: 'z1', state: 'running',
      metrics: { captured_at: new Date(Date.now() - 5 * 60_000).toISOString(), source: 'test' },
    }])

    render(MonitoringView)

    expect(await screen.findByText(/stale measurement/i)).toBeInTheDocument()
  })

  it('refreshes provider metrics every minute and marks the sample stale after two minutes', async () => {
    vi.useFakeTimers()
    const startedAt = new Date('2026-09-23T12:00:00Z')
    vi.setSystemTime(startedAt)
    routeRuntime([{
      qube_id: 'q1', qube_name: 'fresh-vm', zone_id: 'z1', state: 'running',
      metrics: { captured_at: startedAt.toISOString(), source: 'test' },
    }])

    render(MonitoringView)

    expect(await screen.findByText('fresh-vm')).toBeInTheDocument()
    const runtimeRequests = () => apiFetch.mock.calls.filter(([path]) => path === '/monitoring/qubes').length
    const overviewRequests = () => apiFetch.mock.calls.filter(([path]) => path === '/monitoring').length
    expect(runtimeRequests()).toBe(1)
    expect(overviewRequests()).toBe(1)
    expect(screen.queryByText(/stale measurement/i)).not.toBeInTheDocument()

    await vi.advanceTimersByTimeAsync(60_000)
    expect(runtimeRequests()).toBe(2)
    expect(overviewRequests()).toBe(2)
    expect(screen.queryByText(/stale measurement/i)).not.toBeInTheDocument()

    await vi.advanceTimersByTimeAsync(75_000)
    expect(runtimeRequests()).toBe(3)
    expect(overviewRequests()).toBe(3)
    expect(screen.getByText(/stale measurement/i)).toBeInTheDocument()
  })

  it('stops polling when the view is closed', async () => {
    vi.useFakeTimers()
    routeRuntime([])

    const view = render(MonitoringView)
    expect(await screen.findByText(/no qubes are recorded/i)).toBeInTheDocument()
    const before = apiFetch.mock.calls.length
    view.unmount()

    await vi.advanceTimersByTimeAsync(5 * 60_000)
    expect(apiFetch.mock.calls.length).toBe(before)
  })

  it.each([
    ['invalid', 'not-a-timestamp'],
    ['more than 30 seconds in the future', new Date(Date.now() + 60_000).toISOString()],
  ])('marks a %s provider timestamp stale', async (_label, capturedAt) => {
    routeRuntime([{
      qube_id: 'q1', qube_name: 'bad-clock-vm', zone_id: 'z1', state: 'running',
      metrics: { captured_at: capturedAt, source: 'test' },
    }])

    render(MonitoringView)

    expect(await screen.findByText(/stale measurement/i)).toBeInTheDocument()
  })

  it('keeps the last reading beside a refresh failure and lets it age into stale', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-23T12:00:00Z'))
    let runtimeUp = true
    apiFetch.mockImplementation(async (path) => {
      if (path !== '/monitoring/qubes') return monitoringResponse({ metrics: {}, alerts: [] })
      if (!runtimeUp) return failedResponse()
      return monitoringResponse({
        items: [{
          qube_id: 'q1', qube_name: 'was-measured', zone_id: 'z1', state: 'running',
          metrics: { captured_at: new Date().toISOString(), source: 'test', cpu_fraction: 0.1 },
        }],
      })
    })

    render(MonitoringView)
    expect(await screen.findByText('was-measured')).toBeInTheDocument()

    runtimeUp = false
    await vi.advanceTimersByTimeAsync(60_000)

    expect(screen.getByRole('alert')).toHaveTextContent(/provider runtime metrics are unavailable; showing the last successful reading/i)
    expect(screen.getByText('was-measured')).toBeInTheDocument()
    expect(screen.queryByText(/stale measurement/i)).not.toBeInTheDocument()

    await vi.advanceTimersByTimeAsync(75_000)
    expect(screen.getByText(/stale measurement/i), 'a kept reading is marked stale once it is old').toBeInTheDocument()
  })

  it('shows a first provider failure without inventing an empty fleet', async () => {
    apiFetch.mockImplementation(async (path) => path === '/monitoring/qubes'
      ? failedResponse()
      : monitoringResponse({ metrics: {}, alerts: [] }))

    render(MonitoringView)

    expect(await screen.findByText(/provider runtime metrics are unavailable/i)).toBeInTheDocument()
    expect(screen.queryByText(/no qubes are recorded/i)).not.toBeInTheDocument()
  })

  it('renders a null provider value as missing, never as 0.00%', async () => {
    routeRuntime([{
      qube_id: 'q1', qube_name: 'null-vm', zone_id: 'z1', state: 'running',
      metrics: {
        captured_at: new Date().toISOString(), source: 'test',
        cpu_fraction: null, memory_used_bytes: null, memory_max_bytes: 1024,
        disk_read_bytes: null, disk_write_bytes: 1, network_in_bytes: 1, network_out_bytes: null,
      },
    }])

    render(MonitoringView)

    expect(await screen.findByText('null-vm')).toBeInTheDocument()
    expect(screen.getByText('CPU —')).toBeInTheDocument()
    expect(screen.getByText('Memory —')).toBeInTheDocument()
    expect(screen.getByText('Disk I/O since start —')).toBeInTheDocument()
    expect(screen.getByText('Network since start —')).toBeInTheDocument()
    expect(screen.queryByText(/0\.00%/)).not.toBeInTheDocument()
  })

  it('clamps a provider CPU reading above 1 for display only', async () => {
    routeRuntime([{
      qube_id: 'q1', qube_name: 'busy-vm', zone_id: 'z1', state: 'running',
      metrics: { captured_at: new Date().toISOString(), source: 'test', cpu_fraction: 1.04 },
    }])

    render(MonitoringView)

    expect(await screen.findByText('busy-vm')).toBeInTheDocument()
    expect(screen.getByText('CPU 100.00%')).toBeInTheDocument()
  })

  it('explains a VM held by a provider task', async () => {
    routeRuntime([{ qube_id: 'q1', qube_name: 'backup-vm', zone_id: 'z1', state: 'running', reason: 'provider_busy' }])

    render(MonitoringView)

    expect(await screen.findByText(/a provider task \(for example a backup\) holds this VM/i)).toBeInTheDocument()
  })
})

describe('MonitoringView host metric honesty', () => {
  it('labels host metrics with their source and scope', async () => {
    apiFetch.mockResolvedValue(
      monitoringResponse({
        metrics: { cpuUsage: 12.3456, diskUsage: 0, source: 'console-host-linux', capturedAt: '2026-09-23T12:00:00Z' },
        alerts: [],
        note: 'Host-wide Console metrics; these values do not describe managed qubes.',
      }),
    )

    render(MonitoringView)

    expect(await screen.findByText(/do not describe managed qubes/i)).toBeInTheDocument()
    expect(screen.getByText(/console-host-linux/)).toBeInTheDocument()
    expect(screen.getByText('12.35%')).toBeInTheDocument()
    expect(screen.getByText('0.00%'), 'a measured zero is still shown').toBeInTheDocument()
    expect(screen.queryByText(/placeholder metrics/i)).not.toBeInTheDocument()
  })

  it('renders unavailable host metrics as missing instead of zero', async () => {
    apiFetch.mockResolvedValue(
      monitoringResponse({
        metrics: { cpuUsage: null, memoryUsage: null, diskUsage: null, networkIn: null, networkOut: null,
          source: 'console-host-linux', reason: 'awaiting a second sample' },
        alerts: [],
        note: 'Host-wide Console metrics; these values do not describe managed qubes.',
      }),
    )

    render(MonitoringView)

    expect(await screen.findByText('Console host')).toBeInTheDocument()
    expect(screen.getAllByText('—')).toHaveLength(3)
    expect(screen.getByText('↓ —')).toBeInTheDocument()
    expect(screen.getByText('↑ —')).toBeInTheDocument()
    expect(screen.getByText(/awaiting a second sample/i)).toBeInTheDocument()
    expect(screen.queryByText(/0\.00%/)).not.toBeInTheDocument()
  })
})

describe('MonitoringView alerts', () => {
  it('says alerting is not implemented instead of claiming the fleet is healthy', async () => {
    apiFetch.mockResolvedValue(monitoringResponse({ metrics: {}, alerts: [], alerts_status: 'not_implemented' }))

    render(MonitoringView)

    expect(await screen.findByText(/alerting is not implemented yet/i)).toBeInTheDocument()
    expect(screen.queryByText(/all systems operational/i)).not.toBeInTheDocument()
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
      }),
    )

    render(MonitoringView)

    expect(await screen.findByText('zone lab is unreachable')).toBeInTheDocument()
    expect(screen.getByText('CRITICAL')).toBeInTheDocument()
    expect(screen.getByText('Source: agent-probe')).toBeInTheDocument()
  })

  it('does not invent an observation time when an alert has no timestamp', async () => {
    apiFetch.mockResolvedValue(
      monitoringResponse({
        metrics: {},
        alerts: [{ id: 'a2', severity: 'critical', message: 'Agent unavailable', source: 'agent-health' }],
      }),
    )

    render(MonitoringView)

    expect(await screen.findByText(/observation time unavailable/i)).toBeInTheDocument()
  })
})

describe('MonitoringView failure handling', () => {
  it('keeps the last host values on screen when a later poll fails', async () => {
    vi.useFakeTimers()
    let overviewUp = true
    apiFetch.mockImplementation(async (path) => {
      if (path === '/monitoring/qubes') return monitoringResponse({ items: [] })
      return overviewUp
        ? monitoringResponse({ metrics: { cpuUsage: 42, source: 'console-host-linux', capturedAt: '2026-09-23T12:00:00Z' }, alerts: [] })
        : failedResponse()
    })

    render(MonitoringView)
    expect(await screen.findByText('42.00%')).toBeInTheDocument()

    overviewUp = false
    await vi.advanceTimersByTimeAsync(60_000)

    expect(screen.getByText('42.00%'), 'the last good value stays').toBeInTheDocument()
    expect(screen.getByText(/refresh failed: failed to load monitoring data; showing the last values/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i }), 'the view is not replaced by the error').not.toBeInTheDocument()

    overviewUp = true
    await vi.advanceTimersByTimeAsync(60_000)
    expect(screen.queryByText(/refresh failed/i)).not.toBeInTheDocument()
  })

  it('reports a failed metrics load and recovers on retry', async () => {
    // A silently empty view would read as "nothing to report"; the failure has to
    // be stated and the retry has to actually re-ask.
    let overviewUp = false
    apiFetch.mockImplementation(async (path) => {
      if (path === '/monitoring/qubes') return monitoringResponse({ items: [] })
      return overviewUp ? monitoringResponse({ metrics: { cpuUsage: 1 }, alerts: [] }) : failedResponse()
    })

    render(MonitoringView)

    expect(await screen.findByText(/error: failed to load monitoring data/i)).toBeInTheDocument()
    overviewUp = true
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))

    expect(await screen.findByText('Console host')).toBeInTheDocument()
    expect(apiFetch.mock.calls.filter(([path]) => path === '/monitoring')).toHaveLength(2)
  })
})
