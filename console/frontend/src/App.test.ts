import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import App from './App.svelte'
import { auth } from './lib/auth.svelte'

// The shell is not rendered until the server has said which scope this browser
// session has; a zone-scoped session then never lands on a fleet-only view.
// fetch is answered by URL so every view the shell mounts gets a well-formed,
// empty answer and only the session endpoint varies.

type SessionAnswer = { status: number; body: unknown } | Error

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function stubServer(session: () => SessionAnswer): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/api/v1/session') {
      const answer = session()
      if (answer instanceof Error) throw answer
      return jsonResponse(answer.status, answer.body)
    }
    if (url === '/health') {
      return jsonResponse(200, { status: 'ok', version: 'test' })
    }
    return jsonResponse(200, { qubes: [], zones: [], jobs: [], total: 0 })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const zoneSession = { status: 200, body: { subject: 'zone-a-control', scope: 'control', zones: ['zone-a'] } }
const fleetSession = { status: 200, body: { subject: 'api_token', scope: 'control', zones: [] } }

beforeEach(() => {
  auth.rejected = false
  auth.initialized = false
  auth.zones = []
  window.location.hash = ''
})

afterEach(() => {
  vi.unstubAllGlobals()
  window.location.hash = ''
})

describe('App session scope gate', () => {
  it('waits for the session scope before rendering the shell', async () => {
    let release: (answer: SessionAnswer) => void = () => {}
    const pending = new Promise<SessionAnswer>(resolve => { release = resolve })
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/v1/session') {
        const answer = await pending
        if (answer instanceof Error) throw answer
        return jsonResponse(answer.status, answer.body)
      }
      return jsonResponse(200, { qubes: [], zones: [], jobs: [], total: 0 })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(App)

    expect(screen.getByRole('status')).toHaveTextContent(/checking session permissions/i)
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual(['/api/v1/session'])

    release(fleetSession)
    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument()
  })

  it('sends a zone-scoped session from a fleet-only view to the dashboard', async () => {
    window.location.hash = '#monitoring'
    stubServer(() => zoneSession)
    render(App)

    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument()
    expect(window.location.hash).toBe('#dashboard')
    expect(screen.getByRole('button', { name: /monitoring, unavailable/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Settings' })).toBeEnabled()
  })

  it('keeps a zone-scoped session off the desktop approval queue', async () => {
    window.location.hash = '#desktop'
    const fetchMock = stubServer(() => zoneSession)
    render(App)

    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument()
    expect(window.location.hash).toBe('#dashboard')
    expect(screen.getByRole('button', { name: /desktop access, unavailable/i })).toBeDisabled()
    expect(fetchMock.mock.calls.map(([url]) => String(url))).not.toContain('/api/v1/desktop-access')
  })

  it('opens the desktop approval queue for a fleet-wide session', async () => {
    window.location.hash = '#desktop'
    stubServer(() => fleetSession)
    render(App)

    expect(await screen.findByRole('heading', { name: 'Desktop access' })).toBeInTheDocument()
    expect(window.location.hash).toBe('#desktop')
  })

  it('keeps a fleet-wide session on the view its address names', async () => {
    window.location.hash = '#jobs'
    stubServer(() => fleetSession)
    render(App)

    expect(await screen.findByRole('button', { name: 'Jobs' })).toBeEnabled()
    expect(window.location.hash).toBe('#jobs')
    expect(screen.queryByRole('heading', { name: 'Overview' })).not.toBeInTheDocument()
  })

  it('shows the login gate when there is no live session', async () => {
    stubServer(() => ({ status: 401, body: { error: 'Unauthorized', code: 401 } }))
    render(App)

    expect(await screen.findByRole('button', { name: /unlock console/i })).toBeInTheDocument()
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument()
  })

  it('offers a retry instead of guessing a scope when the check fails', async () => {
    let answer: SessionAnswer = new TypeError('Failed to fetch')
    stubServer(() => answer)
    render(App)

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not verify/i)
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument()

    answer = zoneSession
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument()
    expect(auth.zones).toEqual(['zone-a'])
  })
})
