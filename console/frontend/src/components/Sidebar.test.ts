import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import Sidebar from './Sidebar.svelte'

// A zone-scoped credential is refused every fleet-only endpoint. The sidebar
// greys those views out instead of offering a page of 403s; the server still
// enforces the rule, so this is presentation only.
describe('Sidebar zone scope', () => {
  const unavailable = (label: string) => `${label}, unavailable for this zone-scoped credential`

  it('disables fleet-only views and keeps zone-addressable views and settings available', () => {
    render(Sidebar, { props: { currentView: 'dashboard', onViewChange: vi.fn(), zoneScoped: true } })

    for (const label of ['Jobs', 'Desktop access', 'Credentials', 'Billing', 'Monitoring']) {
      expect(screen.getByRole('button', { name: unavailable(label) })).toBeDisabled()
    }
    for (const label of ['Dashboard', 'Qubes', 'Zones', 'Settings']) {
      expect(screen.getByRole('button', { name: label })).toBeEnabled()
    }
  })

  it('does not navigate to a disabled view', async () => {
    const onViewChange = vi.fn()
    render(Sidebar, { props: { currentView: 'dashboard', onViewChange, zoneScoped: true } })

    await userEvent.click(screen.getByRole('button', { name: unavailable('Monitoring') }))
    expect(onViewChange).not.toHaveBeenCalled()

    await userEvent.click(screen.getByRole('button', { name: 'Qubes' }))
    expect(onViewChange).toHaveBeenCalledWith('qubes')
  })

  it('keeps every view enabled for a fleet-wide credential', () => {
    render(Sidebar, { props: { currentView: 'dashboard', onViewChange: vi.fn(), zoneScoped: false } })

    for (const label of ['Jobs', 'Desktop access', 'Credentials', 'Billing', 'Monitoring', 'Settings']) {
      expect(screen.getByRole('button', { name: label })).toBeEnabled()
    }
  })
})
