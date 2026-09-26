import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import QubeTable from './QubeTable.svelte'
import type { Qube, Zone } from '../lib/types'

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

function renderTable(qube: Qube, zones: Zone[] = [], onedit = vi.fn()): void {
  render(QubeTable, { props: { qubes: [qube], jobs: {}, zones, onedit } })
}

// "unreachable" answers whether the agent answers; agent_recovery answers
// whether waiting can still fix it. The list is where an operator scans for
// trouble, so the second reading has to be visible there — and, just as
// important, absent when it does not apply.
describe('QubeTable agent recovery state', () => {
  it('marks an agent that has outlasted the unit restart budget', () => {
    renderTable(qubeFixture({
      status: 'running',
      agent_health: 'unreachable',
      agent_recovery: 'manual',
      agent_failing_since: '2026-09-22T10:00:00Z',
      agent_last_error: 'nothing is listening on 10.0.0.7:8443',
    }))

    const marker = screen.getByText(/manual recovery/i)
    expect(marker).toBeInTheDocument()
    // The tooltip is where the limit of what the console knows is stated: it
    // must not read as "the console saw the unit fail".
    expect(marker.getAttribute('title')).toMatch(/cannot read the unit inside the qube/i)
    expect(marker.getAttribute('title')).toMatch(/systemctl status qubes-air-agent/)
    expect(marker.getAttribute('title')).toMatch(/docs\/runbook-remotevm\.md/)
    // Nor as "the agent is gone for good": the unit is enabled and starts again
    // at boot, so the supportable claim is that it stopped restarting itself
    // within this boot and needs its failed state cleared.
    expect(marker.getAttribute('title')).toMatch(/in this boot it has stopped restarting/i)
    expect(marker.getAttribute('title')).toMatch(/systemctl reset-failed/)
    expect(marker.getAttribute('title')).not.toMatch(/will not come back on its own/i)
  })

  // A parked qube keeps whatever agent reading it had before it was parked, and
  // nothing probes it any more (backend: computeRunning in qube_predicates.go).
  // Rendering that leftover as a live problem would tell an operator to run
  // recovery steps on a qube that is not even up.
  it.each(['suspended', 'released', 'stopped'] as const)(
    'does not mark a parked qube (%s) that kept a manual reading',
    (status) => {
      renderTable(qubeFixture({
        status,
        agent_health: 'unreachable',
        agent_recovery: 'manual',
        agent_failing_since: '2026-09-22T10:00:00Z',
      }))

      expect(screen.getByText('qube-one')).toBeInTheDocument()
      expect(screen.queryByText(/manual recovery/i)).not.toBeInTheDocument()
    },
  )

  // The mirror of the parked case, and the reason the guard is a status
  // predicate rather than `status === 'running'`: the backend probes qubes that
  // are still coming up, so a transient status can carry a real reading.
  it('still marks a qube that is coming up and failing', () => {
    renderTable(qubeFixture({
      status: 'resuming',
      agent_health: 'unreachable',
      agent_recovery: 'manual',
      agent_failing_since: '2026-09-22T10:00:00Z',
    }))

    expect(screen.getByText(/manual recovery/i)).toBeInTheDocument()
  })

  it('does not mark a failure that may still be a restart in flight', () => {
    renderTable(qubeFixture({
      status: 'running',
      agent_health: 'unreachable',
      agent_recovery: 'pending',
      agent_last_error: 'nothing is listening on 10.0.0.7:8443',
    }))

    expect(screen.getByText(/^unreachable$/)).toBeInTheDocument()
    expect(screen.queryByText(/manual recovery/i)).not.toBeInTheDocument()
  })

  it('does not mark a healthy agent', () => {
    renderTable(qubeFixture({
      status: 'running',
      agent_health: 'healthy',
      agent_recovery: 'none',
    }))

    expect(screen.getByText(/^healthy$/)).toBeInTheDocument()
    expect(screen.queryByText(/manual recovery/i)).not.toBeInTheDocument()
  })
})

describe('QubeTable agent health', () => {
  // The backend reports "starting" while a freshly booted qube is inside its
  // grace period (models.AgentHealthStarting). It is neither a fault nor an
  // absence of information, so it must not read as either.
  it('shows a booting agent as starting, not unknown or unreachable', () => {
    renderTable(qubeFixture({ status: 'creating', agent_health: 'starting', agent_recovery: 'none' }))

    const pill = screen.getByText(/^starting$/)
    expect(pill).toHaveClass('agent', 'starting')
    expect(screen.queryByText(/^unknown$/)).not.toBeInTheDocument()
    expect(screen.queryByText(/^unreachable$/)).not.toBeInTheDocument()
  })

  it('shows a value it does not know as unknown', () => {
    renderTable(qubeFixture({ agent_health: undefined }))

    expect(screen.getByText(/^unknown$/)).toBeInTheDocument()
  })
})

describe('QubeTable desktop apps', () => {
  // The menu and the launch both go through the agent, so there is nothing to
  // ask while compute is down, starting up, or being purged.
  it.each(['suspended', 'released', 'stopped', 'creating', 'resuming', 'error'] as const)(
    'offers no app menu for a %s qube',
    (status) => {
      renderTable(qubeFixture({ status }))

      expect(screen.getByText('qube-one')).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: 'Apps' })).not.toBeInTheDocument()
    },
  )

  it('offers no app menu for a running qube with a purge pending', () => {
    renderTable(qubeFixture({ status: 'running', purge_requested: true }))

    expect(screen.queryByRole('button', { name: 'Apps' })).not.toBeInTheDocument()
  })

  it('offers the app menu for a running qube', () => {
    renderTable(qubeFixture({ status: 'running' }))

    expect(screen.getByRole('button', { name: 'Apps' })).toBeEnabled()
  })

  // Agent health is a periodic probe and "unknown" whenever probing is off, so
  // it is not a gate: the menu request itself reports an unreachable agent.
  it.each(['unknown', 'unreachable', 'starting'] as const)(
    'offers the app menu for a running qube whose agent reads %s',
    (health) => {
      renderTable(qubeFixture({ status: 'running', agent_health: health }))

      expect(screen.getByRole('button', { name: 'Apps' })).toBeEnabled()
    },
  )
})

describe('QubeTable row', () => {
  it('names the zone and node the qube is placed on', () => {
    renderTable(
      qubeFixture({ zone_id: 'z1', spec: { vcpu: 2, memory: 2048, disk: 20, node: 'pve-2' } }),
      [{ id: 'z1', name: 'lab', type: 'proxmox', status: 'connected', config: { endpoint: '' },
        created_at: '', updated_at: '' }],
    )

    const zoneCell = screen.getByText('lab', { exact: false })
    expect(zoneCell).toHaveTextContent(/^lab\s*· pve-2$/)
  })

  it('hands the row qube to the edit callback', async () => {
    const onedit = vi.fn()
    const qube = qubeFixture({ status: 'stopped' })
    renderTable(qube, [], onedit)

    await userEvent.click(screen.getByRole('button', { name: /^edit$/i }))

    expect(onedit).toHaveBeenCalledWith(qube)
  })

  it('disables edit and release while an operation is in flight', () => {
    renderTable(qubeFixture({ status: 'creating' }))

    expect(screen.getByRole('button', { name: /^edit$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /^release$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Provisioning…' })).toBeDisabled()
  })

  // Released already means "compute gone, data kept"; releasing again has
  // nothing to act on. Purge is the only way forward from there.
  it('disables release on a released qube and offers purge instead', () => {
    renderTable(qubeFixture({ status: 'released' }))

    expect(screen.getByRole('button', { name: /^release$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /^edit$/i })).toBeEnabled()
    expect(screen.getByRole('button', { name: /^purge$/i })).toBeEnabled()
    expect(screen.getByRole('button', { name: /^resume$/i })).toBeEnabled()
  })

  // A pending purge is irreversible intent: the qube must not be edited,
  // released or resumed underneath it, only purged again.
  it('disables edit and release while a purge is pending', () => {
    renderTable(qubeFixture({ status: 'stopped', purge_requested: true }))

    expect(screen.getByRole('button', { name: /^edit$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /^release$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Purge pending' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: /^(start|resume)$/i })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^retry purge$/i })).toBeEnabled()
  })
})
