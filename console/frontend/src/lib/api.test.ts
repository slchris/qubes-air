import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { apiFetch, getApiBaseUrl, login, logout } from './api'
import { auth } from './auth.svelte'

// The API layer exchanges the long-lived token for a session cookie and then
// relies on that cookie. These tests pin the two things that are easy to get
// wrong: the request must send credentials, and the token must be sent once,
// not kept.

function jsonResponse(status: number, body: unknown = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  auth.rejected = false
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('getApiBaseUrl', () => {
  it('defaults to the relative /api/v1 prefix', () => {
    expect(getApiBaseUrl()).toBe('/api/v1')
  })
})

describe('login', () => {
  it('exchanges the token for a session and clears the rejected state', async () => {
    auth.markRejected()
    fetchMock.mockResolvedValue(jsonResponse(200, { subject: 'operator' }))

    await login('secret-token')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/session')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
    expect(JSON.parse(init.body)).toEqual({ token: 'secret-token' })
    expect(auth.required).toBe(false)
  })

  it('marks the gate rejected and throws when the token is refused', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401))

    await expect(login('wrong')).rejects.toThrow()
    expect(auth.required).toBe(true)
    expect(auth.wasRejected).toBe(true)
  })
})

describe('logout', () => {
  it('ends the session with credentials included and raises the gate', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))

    await logout()

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/session')
    expect(init.method).toBe('DELETE')
    expect(init.credentials).toBe('include')
    expect(auth.required).toBe(true)
  })
})

describe('apiFetch', () => {
  it('sends the session cookie on same-origin calls', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { data: [] }))

    await apiFetch('/zones')

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/zones')
    expect(init.credentials).toBe('include')
  })
})
