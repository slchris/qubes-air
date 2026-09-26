import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import BillingView from './BillingView.svelte'
import * as api from '../lib/api'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, apiFetch: vi.fn() }
})

const apiFetch = vi.mocked(api.apiFetch)

function response(body: Record<string, unknown>): Response {
  return { ok: true, status: 200, json: async () => body } as Response
}

beforeEach(() => apiFetch.mockReset())

describe('BillingView', () => {
  it('labels placeholder totals as estimates and renders usage rows', async () => {
    apiFetch.mockResolvedValue(response({
      placeholder: true,
      note: 'No cloud billing feed is connected.',
      summary: { currentMonth: 12.5, lastMonth: 9, projectedMonth: 15, currency: 'USD' },
      usage: [{ service: 'compute', usage: 4, unit: 'hours', cost: 12.5 }],
    }))

    render(BillingView)

    expect(await screen.findByText('Not a bill.')).toBeInTheDocument()
    expect(screen.getByText('No cloud billing feed is connected.')).toBeInTheDocument()
    expect(screen.getAllByText('$12.50')).toHaveLength(2)
    expect(screen.getByText('4 hours')).toBeInTheDocument()
  })

  it('shows no figures when the billing endpoint refuses', async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 503, json: async () => ({}) } as Response)

    render(BillingView)

    expect(await screen.findByText(/failed to load billing/i)).toBeInTheDocument()
    expect(screen.queryByText(/\$\d/)).not.toBeInTheDocument()
  })

  it('shows API failure and lets the operator retry successfully', async () => {
    apiFetch.mockRejectedValueOnce(new Error('billing service unavailable'))
      .mockResolvedValueOnce(response({ placeholder: true, summary: null, usage: [] }))

    render(BillingView)

    expect(await screen.findByText(/billing service unavailable/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Not a bill.')).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledTimes(2)
  })
})
