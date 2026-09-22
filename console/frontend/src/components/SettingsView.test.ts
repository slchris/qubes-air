import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import SettingsView from './SettingsView.svelte'
import * as api from '../lib/api'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, login: vi.fn(), logout: vi.fn() }
})

const login = vi.mocked(api.login)
const logout = vi.mocked(api.logout)

beforeEach(() => {
  login.mockReset()
  logout.mockReset()
  // The component loads settings on mount; give it a well-formed answer so the
  // session controls are the only thing under test.
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ settings: {} }),
    }),
  )
})

describe('SettingsView session controls', () => {
  it('starts a session from the entered token and clears the field', async () => {
    login.mockResolvedValue(undefined)
    render(SettingsView)

    await userEvent.type(await screen.findByLabelText(/api token/i), '  deploy-token  ')
    await userEvent.click(await screen.findByRole('button', { name: /start session/i }))

    expect(login).toHaveBeenCalledWith('deploy-token')
    expect(await screen.findByText(/session started in this browser/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/api token/i)).toHaveValue('')
  })

  it('reports a rejected token without leaving the field in a broken state', async () => {
    login.mockRejectedValue(new Error('rejected'))
    render(SettingsView)

    await userEvent.type(await screen.findByLabelText(/api token/i), 'stale-token')
    await userEvent.click(await screen.findByRole('button', { name: /start session/i }))

    expect(await screen.findByText(/token rejected/i)).toBeInTheDocument()
  })

  it('does not call the API for an empty token', async () => {
    render(SettingsView)

    await userEvent.click(await screen.findByRole('button', { name: /start session/i }))
    expect(login).not.toHaveBeenCalled()
  })

  it('signs out and reports it', async () => {
    logout.mockResolvedValue(undefined)
    render(SettingsView)

    await userEvent.click(await screen.findByRole('button', { name: /sign out/i }))

    expect(logout).toHaveBeenCalledTimes(1)
    expect(await screen.findByText(/signed out/i)).toBeInTheDocument()
  })
})
