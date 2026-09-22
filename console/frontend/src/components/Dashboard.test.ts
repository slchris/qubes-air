import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'

import Dashboard from './Dashboard.svelte'
import * as api from '../lib/api'
import { qubeStore } from '../lib/stores'
import type { Qube } from '../lib/types'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return {
    ...actual,
    listQubes: vi.fn(),
    listZones: vi.fn(),
    listJobs: vi.fn(),
  }
})

const listQubes = vi.mocked(api.listQubes)

function qubeFixture(overrides: Partial<Qube> = {}): Qube {
  return {
    purge_requested: false,
    id: 'q1',
    name: 'qube-one',
    type: 'dev',
    status: 'running',
    spec: { vcpu: 1, memory: 512, disk: 20 },
    created_at: '2026-09-21T00:00:00Z',
    updated_at: '2026-09-21T00:00:00Z',
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(api.listZones).mockResolvedValue({ zones: [], total: 0 } as never)
  vi.mocked(api.listJobs).mockResolvedValue({ jobs: [], total: 0 } as never)
})

afterEach(() => {
  vi.restoreAllMocks()
})

async function renderWithQubes(qubes: Qube[]): Promise<void> {
  listQubes.mockResolvedValue({ qubes, total: qubes.length } as never)
  await qubeStore.load()
  render(Dashboard)
  // A name can legitimately appear twice — in the alert's name list and in the
  // qube rows — so wait for at least one rather than for a unique match.
  expect((await screen.findAllByText(qubes[0].name)).length).toBeGreaterThan(0)
}

// The landing view is where "something is wrong" has to become "this is what
// you do about it". An unreachable agent that may still be restarting and one
// that has outlasted every automatic restart look identical unless the count is
// spelled out here.
describe('Dashboard agent recovery alert', () => {
  it('says how many unreachable agents are past the unit restart budget', async () => {
    await renderWithQubes([
      qubeFixture({ id: 'q1', name: 'gave-up', agent_health: 'unreachable', agent_recovery: 'manual' }),
      qubeFixture({ id: 'q2', name: 'maybe-restarting', agent_health: 'unreachable', agent_recovery: 'pending' }),
    ])

    expect(screen.getByText(/qubes are running but their agent is unreachable/i)).toBeInTheDocument()
    expect(screen.getByText(/1 of them past the agent unit's restart budget/i)).toBeInTheDocument()
  })

  it('does not claim manual recovery when every failure may still be restarting', async () => {
    await renderWithQubes([
      qubeFixture({ id: 'q1', name: 'maybe-restarting', agent_health: 'unreachable', agent_recovery: 'pending' }),
    ])

    expect(screen.getByText(/qube is running but their agent is unreachable/i)).toBeInTheDocument()
    expect(screen.queryByText(/restart budget/i)).not.toBeInTheDocument()
  })

  // The count is derived from the running qubes, so a parked qube's leftover
  // reading never reaches it. Regression guard for the same defect the QubeList
  // badge had: recovery steps pointed at a qube that is not up.
  it('does not count a parked qube that kept a manual reading', async () => {
    await renderWithQubes([
      qubeFixture({
        id: 'q1', name: 'parked', status: 'suspended',
        agent_health: 'unreachable', agent_recovery: 'manual',
        agent_failing_since: '2026-09-22T10:00:00Z',
      }),
    ])

    expect(screen.queryByText(/manual recovery/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/restart budget/i)).not.toBeInTheDocument()
  })
})
