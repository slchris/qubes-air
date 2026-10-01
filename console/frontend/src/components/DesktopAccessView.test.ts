import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import DesktopAccessView from './DesktopAccessView.svelte'
import {
  ApiException,
  approveDesktopAccessRequest,
  denyDesktopAccessRequest,
  listDesktopAccessRequests,
  listQubes,
  stopDesktopAccessRequest,
} from '../lib/api'
import type { DesktopAccessRequest } from '../lib/types'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return {
    ApiException: actual.ApiException,
    approveDesktopAccessRequest: vi.fn(),
    denyDesktopAccessRequest: vi.fn(),
    listDesktopAccessRequests: vi.fn(),
    listQubes: vi.fn(),
    stopDesktopAccessRequest: vi.fn(),
  }
})

const pending: DesktopAccessRequest = {
  id: 'request-1',
  subject: 'mcp-client',
  qube_id: 'qube-1',
  operation: 'frame',
  state: 'pending',
  expires_at: '2026-09-26T12:00:30Z',
}
const active: DesktopAccessRequest = { ...pending, state: 'active' }

beforeEach(() => {
  vi.mocked(listDesktopAccessRequests).mockResolvedValue([pending])
  vi.mocked(listQubes).mockResolvedValue({ qubes: [{ id: 'qube-1', name: 'finance-desktop' } as never], total: 1 })
  vi.mocked(approveDesktopAccessRequest).mockResolvedValue({ ...pending, state: 'approved' })
  vi.mocked(denyDesktopAccessRequest).mockResolvedValue(pending)
  vi.mocked(stopDesktopAccessRequest).mockResolvedValue()
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('DesktopAccessView', () => {
  it('shows who asks to read which qube, with Allow and Deny', async () => {
    render(DesktopAccessView)

    expect(await screen.findByRole('heading', { name: 'Read screen from finance-desktop' })).toBeInTheDocument()
    expect(screen.getByText('mcp-client')).toBeInTheDocument()
    expect(screen.getByText('qube-1')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Allow reading the screen of finance-desktop' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Deny the screen request for finance-desktop' })).toBeEnabled()
  })

  it('allows a request and says what the requester may now do', async () => {
    render(DesktopAccessView)
    await userEvent.click(await screen.findByRole('button', { name: 'Allow reading the screen of finance-desktop' }))

    expect(approveDesktopAccessRequest).toHaveBeenCalledWith('request-1')
    expect(await screen.findByRole('status')).toHaveTextContent('mcp-client can read one frame of finance-desktop within 30 seconds')
  })

  it('denies a request', async () => {
    render(DesktopAccessView)
    await userEvent.click(await screen.findByRole('button', { name: 'Deny the screen request for finance-desktop' }))

    expect(denyDesktopAccessRequest).toHaveBeenCalledWith('request-1')
    expect(await screen.findByRole('status')).toHaveTextContent('Request denied.')
  })

  it('offers Stop for a live grant', async () => {
    vi.mocked(listDesktopAccessRequests).mockResolvedValue([active])
    render(DesktopAccessView)
    await userEvent.click(await screen.findByRole('button', { name: 'Stop desktop access to finance-desktop' }))

    expect(stopDesktopAccessRequest).toHaveBeenCalledWith('request-1')
    expect(screen.queryByRole('button', { name: /^Allow/ })).not.toBeInTheDocument()
  })

  it("shows the server's reason when the session may not review requests", async () => {
    vi.mocked(listDesktopAccessRequests).mockRejectedValue(
      new ApiException(403, 'UNKNOWN_ERROR', 'desktop approvals require a fleet-wide control session in the Console'),
    )
    render(DesktopAccessView)

    expect(await screen.findByRole('alert')).toHaveTextContent('desktop approvals require a fleet-wide control session')
  })

  it("shows the server's reason when a decision is refused", async () => {
    vi.mocked(approveDesktopAccessRequest).mockRejectedValue(
      new ApiException(412, 'UNKNOWN_ERROR', 'the qube must be running with a healthy agent'),
    )
    render(DesktopAccessView)
    await userEvent.click(await screen.findByRole('button', { name: 'Allow reading the screen of finance-desktop' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('the qube must be running with a healthy agent')
  })

  it('keeps the queue usable when qube names cannot be loaded', async () => {
    vi.mocked(listQubes).mockRejectedValue(new Error('offline'))
    render(DesktopAccessView)

    expect(await screen.findByRole('heading', { name: 'Read screen from qube-1' })).toBeInTheDocument()
  })

  it('polls the queue and looks up qube names only for unseen qubes', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      render(DesktopAccessView)
      await screen.findByRole('heading', { name: 'Read screen from finance-desktop' })
      await vi.advanceTimersByTimeAsync(3000)
      await waitFor(() => expect(listDesktopAccessRequests).toHaveBeenCalledTimes(2))
      expect(listQubes).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('says so when nothing is waiting', async () => {
    vi.mocked(listDesktopAccessRequests).mockResolvedValue([])
    render(DesktopAccessView)

    expect(await screen.findByText('No request is waiting and no desktop grant is live.')).toBeInTheDocument()
  })
})
