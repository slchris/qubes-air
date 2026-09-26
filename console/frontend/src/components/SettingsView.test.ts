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

// The security section is wired now: the timeout governs browser sessions and
// the server refuses what it does not implement, so the page must neither offer
// those switches nor hide the server's reason when a save is refused.
describe('SettingsView security settings', () => {
  function stubSettings(settings: unknown, putResponse?: { ok: boolean; status: number; body: unknown }) {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'PUT' && putResponse) {
        return { ok: putResponse.ok, status: putResponse.status, json: async () => putResponse.body }
      }
      return { ok: true, status: 200, json: async () => ({ settings }) }
    })
    vi.stubGlobal('fetch', fetchMock)
    return fetchMock
  }

  it('keeps email and two-factor switches off and disabled even if the server says otherwise', async () => {
    stubSettings({ notifications: { email: true }, security: { sessionTimeout: 30, twoFactorEnabled: true } })
    render(SettingsView)

    const email = await screen.findByLabelText(/email notifications \(not available\)/i)
    const twoFactor = screen.getByLabelText(/two-factor authentication \(not available\)/i)
    expect(email).toBeDisabled()
    expect(email).not.toBeChecked()
    expect(twoFactor).toBeDisabled()
    expect(twoFactor).not.toBeChecked()
  })

  it('describes the timeout as live and bounds the input to 5-1440 minutes', async () => {
    stubSettings({ security: { sessionTimeout: 45 } })
    render(SettingsView)

    const timeout = await screen.findByLabelText(/session timeout/i)
    expect(timeout).toHaveValue(45)
    expect(timeout).toHaveAttribute('min', '5')
    expect(timeout).toHaveAttribute('max', '1440')
    expect(timeout).toBeRequired()
    expect(screen.getByText(/ends every session already older than it, this one included/i)).toBeInTheDocument()
    expect(screen.queryByText(/session lifetime is set by the server's session store/i)).not.toBeInTheDocument()
  })

  it('saves the session timeout with the unimplemented switches off', async () => {
    const fetchMock = stubSettings({ security: { sessionTimeout: 30 } }, { ok: true, status: 200, body: {} })
    render(SettingsView)

    const timeout = await screen.findByLabelText(/session timeout/i)
    await userEvent.clear(timeout)
    await userEvent.type(timeout, '45')
    await userEvent.click(screen.getByRole('button', { name: /save settings/i }))

    expect(await screen.findByText(/settings saved successfully/i)).toBeInTheDocument()
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === 'PUT')
    expect(put).toBeDefined()
    const body = JSON.parse(put?.[1]?.body as string)
    expect(body.security).toEqual({ sessionTimeout: 45, twoFactorEnabled: false })
    expect(body.notifications.email).toBe(false)
  })

  it('shows the reason the server gave for refusing a save', async () => {
    stubSettings({ security: { sessionTimeout: 30 } }, {
      ok: false,
      status: 400,
      body: { error: 'Bad Request', message: 'invalid session timeout: 1441 minutes is outside 5-1440', code: 400 },
    })
    render(SettingsView)

    await userEvent.click(await screen.findByRole('button', { name: /save settings/i }))

    expect(await screen.findByText(/1441 minutes is outside 5-1440/i)).toBeInTheDocument()
    expect(screen.queryByText(/settings saved successfully/i)).not.toBeInTheDocument()
  })
})
