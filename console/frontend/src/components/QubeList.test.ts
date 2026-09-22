import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import { tick } from 'svelte'

import QubeList from './QubeList.svelte'
import * as api from '../lib/api'
import { qubeStore } from '../lib/stores'
import type { Qube } from '../lib/types'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return {
    ...actual,
    listQubes: vi.fn(),
    listZones: vi.fn(),
    getQube: vi.fn(),
    purgeQube: vi.fn(),
    createQube: vi.fn(),
    getZoneCapacity: vi.fn(),
  }
})

const listQubes = vi.mocked(api.listQubes)
const purgeQube = vi.mocked(api.purgeQube)

function qubeFixture(overrides: Partial<Qube> = {}): Qube {
  return {
    purge_requested: false,
    id: 'q1',
    name: 'qube-one',
    type: 'dev',
    status: 'released',
    spec: { vcpu: 1, memory: 512, disk: 20 },
    created_at: '2026-09-21T00:00:00Z',
    updated_at: '2026-09-21T00:00:00Z',
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(api.listZones).mockResolvedValue({ zones: [], total: 0 } as never)
  vi.mocked(api.getZoneCapacity).mockResolvedValue({} as never)
  purgeQube.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

async function renderWithQube(qube: Qube): Promise<void> {
  listQubes.mockResolvedValue({ qubes: [qube], total: 1 } as never)
  vi.mocked(api.getQube).mockResolvedValue(qube)
  await qubeStore.load()
  render(QubeList)
  expect(await screen.findByText(qube.name)).toBeInTheDocument()
}

describe('QubeList purge confirmation', () => {
  it('purges only after the typed name matches', async () => {
    const qube = qubeFixture()
    purgeQube.mockResolvedValue(undefined)
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const prompt = vi.spyOn(window, 'prompt').mockReturnValue(qube.name)

    await renderWithQube(qube)
    await userEvent.click(screen.getByRole('button', { name: /^purge$/i }))

    expect(window.confirm).toHaveBeenCalledTimes(1)
    expect(prompt).toHaveBeenCalledTimes(1)
    expect(purgeQube).toHaveBeenCalledWith(qube.id, qube.name)
  })

  it('does nothing when the confirmation dialog is dismissed', async () => {
    const qube = qubeFixture()
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const prompt = vi.spyOn(window, 'prompt')

    await renderWithQube(qube)
    await userEvent.click(screen.getByRole('button', { name: /^purge$/i }))

    expect(prompt).not.toHaveBeenCalled()
    expect(purgeQube).not.toHaveBeenCalled()
    await tick()
    expect(screen.getByText(qube.name)).toBeInTheDocument()
  })

  it('cancels and tells the operator when the typed name does not match', async () => {
    const qube = qubeFixture()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    vi.spyOn(window, 'prompt').mockReturnValue('not-the-name')
    const alert = vi.spyOn(window, 'alert').mockImplementation(() => undefined)

    await renderWithQube(qube)
    await userEvent.click(screen.getByRole('button', { name: /^purge$/i }))

    expect(purgeQube).not.toHaveBeenCalled()
    expect(alert).toHaveBeenCalledWith(expect.stringMatching(/did not match/i))
  })

  it('surfaces a backend refusal instead of pretending the purge started', async () => {
    const qube = qubeFixture()
    purgeQube.mockRejectedValue(new api.ApiException(409, 'CONFLICT', 'qube is busy'))
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    vi.spyOn(window, 'prompt').mockReturnValue(qube.name)
    const alert = vi.spyOn(window, 'alert').mockImplementation(() => undefined)

    await renderWithQube(qube)
    await userEvent.click(screen.getByRole('button', { name: /^purge$/i }))

    expect(purgeQube).toHaveBeenCalledTimes(1)
    expect(alert).toHaveBeenCalledWith('qube is busy')
  })

  it('offers no purge button for a running qube', async () => {
    await renderWithQube(qubeFixture({ status: 'running' }))
    expect(screen.queryByRole('button', { name: /^purge$/i })).not.toBeInTheDocument()
  })
})

describe('QubeList create flow', () => {
  it('sends the entered name and closes the dialog on success', async () => {
    listQubes.mockResolvedValue({ qubes: [], total: 0 } as never)
    vi.mocked(api.listZones).mockResolvedValue({
      zones: [{ id: 'z1', name: 'zone-a', type: 'proxmox', status: 'connected' }],
      total: 1,
    } as never)
    const created = qubeFixture({ id: 'q-new', name: 'fresh-qube', status: 'pending' })
    vi.mocked(api.createQube).mockResolvedValue({ qube: created, job_id: 'job-1' } as never)
    vi.mocked(api.getQube).mockResolvedValue(qubeFixture({ id: 'q-new', name: 'fresh-qube', status: 'running' }))

    render(QubeList)
    await userEvent.click(await screen.findByRole('button', { name: /\+ create qube/i }))
    await userEvent.type(screen.getByLabelText(/^name$/i), 'fresh-qube')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))

    expect(api.createQube).toHaveBeenCalledTimes(1)
    expect(api.createQube).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'fresh-qube', zone_id: 'z1' }),
    )
    expect(screen.queryByRole('dialog', { name: /create qube/i })).not.toBeInTheDocument()
  })

  it('keeps the dialog open and shows the backend refusal', async () => {
    listQubes.mockResolvedValue({ qubes: [], total: 0 } as never)
    vi.mocked(api.createQube).mockRejectedValue(
      new api.ApiException(409, 'CONFLICT', 'a qube with that name already exists'),
    )

    render(QubeList)
    await userEvent.click(await screen.findByRole('button', { name: /\+ create qube/i }))
    await userEvent.type(screen.getByLabelText(/^name$/i), 'duplicate')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))

    expect(await screen.findByText(/a qube with that name already exists/i)).toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: /create qube/i })).toBeInTheDocument()
  })
})
