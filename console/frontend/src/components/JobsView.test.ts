import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import JobsView from './JobsView.svelte'
import * as api from '../lib/api'
import type { Job } from '../lib/types'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, listJobs: vi.fn(), getJobLog: vi.fn(), getJob: vi.fn(), streamJobLog: vi.fn() }
})

const listJobs = vi.mocked(api.listJobs)
const getJobLog = vi.mocked(api.getJobLog)
const getJob = vi.mocked(api.getJob)

function jobFixture(overrides: Partial<Job> = {}): Job {
  return {
    id: 'job-1',
    qube_id: 'q1',
    qube_name: 'workstation',
    action: 'provision',
    state: 'succeeded',
    enqueued_at: '2026-09-23T10:00:00Z',
    started_at: '2026-09-23T10:00:02Z',
    finished_at: '2026-09-23T10:00:42Z',
    ...overrides,
  }
}

beforeEach(() => {
  listJobs.mockReset()
  getJobLog.mockReset()
  getJob.mockReset()
  vi.mocked(api.streamJobLog).mockReset()
})

describe('JobsView', () => {
  it('loads the job history and expands a completed job log', async () => {
    const job = jobFixture()
    listJobs.mockResolvedValue({ jobs: [job], count: 1 } as never)
    getJobLog.mockResolvedValue({ offset: 18, data: 'terraform: provision complete\n', running: false, state: 'succeeded' })

    render(JobsView)

    const row = await screen.findByRole('button', { name: /workstation provision succeeded/ })
    expect(row).toBeInTheDocument()
    expect(row).toHaveTextContent('40s')
    await userEvent.click(row)
    await userEvent.click(await screen.findByRole('button', { name: /job log/i }))

    expect(await screen.findByText(/terraform: provision complete/)).toBeInTheDocument()
    expect(getJobLog).toHaveBeenCalledWith(job.id, 0)
  })

  it('shows the stored failure reason when a failed job has no log output', async () => {
    const job = jobFixture({ id: 'job-failed', state: 'failed', error: 'provider rejected the VM request' })
    listJobs.mockResolvedValue({ jobs: [job], count: 1 } as never)
    getJobLog.mockResolvedValue({ offset: 0, data: '', running: false, state: 'failed' })
    getJob.mockResolvedValue(job)

    render(JobsView)
    await userEvent.click(await screen.findByRole('button', { name: /workstation provision failed/ }))

    expect(await screen.findByText('provider rejected the VM request')).toBeInTheDocument()
    expect(screen.getByText('Failed — see log')).toBeInTheDocument()
  })

  it('surfaces a history error and allows a refresh to recover', async () => {
    listJobs.mockRejectedValueOnce(new Error('history unavailable'))
      .mockResolvedValueOnce({ jobs: [], count: 0 } as never)

    render(JobsView)

    expect(await screen.findByText('history unavailable')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    expect(await screen.findByText(/No jobs yet/)).toBeInTheDocument()
  })
})
