import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  ApiException,
  apiFetch,
  approveDesktopAccessRequest,
  denyDesktopAccessRequest,
  getApiBaseUrl,
  getQubeAppMenus,
  launchQubeApp,
  listDesktopAccessRequests,
  listQubes,
  login,
  logout,
  refreshSessionScope,
  responseError,
  stopDesktopAccessRequest,
} from './api'
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
  auth.initialized = false
  auth.zones = []
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
    expect(auth.initialized).toBe(true)
    expect(auth.zoneScoped).toBe(false)
  })

  it('keeps the zone restriction the login answer carries', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { subject: 'zone-operator', scope: 'control', zones: ['z1'] }))

    await login('zone-token')

    expect(auth.zones).toEqual(['z1'])
    expect(auth.zoneScoped).toBe(true)
  })

  it('marks the gate rejected and throws when the token is refused', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401))

    await expect(login('wrong')).rejects.toThrow()
    expect(auth.required).toBe(true)
    expect(auth.wasRejected).toBe(true)
  })
})

describe('refreshSessionScope', () => {
  it('reads the current scope with the session cookie and records its zones', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { subject: 'zone-operator', scope: 'read-only', zones: ['z1'] }))

    await refreshSessionScope()

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/session')
    expect(init.method).toBe('GET')
    expect(init.credentials).toBe('include')
    expect(init.body).toBeUndefined()
    expect(auth.initialized).toBe(true)
    expect(auth.zones).toEqual(['z1'])
    expect(auth.zoneScoped).toBe(true)
  })

  it('treats a fleet-wide answer as unrestricted', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { subject: 'api_token', scope: 'control', zones: [] }))

    await refreshSessionScope()

    expect(auth.zoneScoped).toBe(false)
    expect(auth.required).toBe(false)
  })

  it('raises the gate when there is no live session', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { error: 'Unauthorized', code: 401 }))

    await expect(refreshSessionScope()).rejects.toThrow()
    expect(auth.initialized).toBe(true)
    expect(auth.required).toBe(true)
  })

  it('does not guess a scope when the server cannot answer', async () => {
    fetchMock.mockResolvedValue(jsonResponse(503, { error: 'Service Unavailable', message: 'database is locked' }))

    await expect(refreshSessionScope()).rejects.toThrow('database is locked')
    expect(auth.initialized).toBe(false)
    expect(auth.required).toBe(false)
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

describe('refusals', () => {
  // The console answers a refused request with the HTTP status text in `error`
  // and the reason in `message` (handler.respondError). A spec bound is the case
  // that makes it matter: "Bad Request" tells an operator nothing, while
  // "spec.disk 200000 GB is above the maximum 16384 GB" tells them what to
  // change. Reading `error` alone is why every refusal used to arrive as the
  // former.
  it('surfaces the API reason, not the HTTP status text', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(400, {
        error: 'Bad Request',
        message:
          'invalid qube spec: spec.disk 200000 GB is above the maximum 16384 GB (qube_spec.max_disk_gb)',
        code: 400,
      })
    )

    await expect(listQubes()).rejects.toThrow('above the maximum 16384 GB')
  })

  it('falls back to the status text when the body carries no reason', async () => {
    fetchMock.mockResolvedValue(jsonResponse(500, { error: 'Internal Server Error' }))

    await expect(listQubes()).rejects.toThrow('Internal Server Error')
  })
})

describe('responseError', () => {
  // For raw apiFetch callers: the refusal must carry the server's reason, read
  // the same way the typed calls read it.
  it('reads the reason from message and keeps the status', async () => {
    const err = await responseError(
      jsonResponse(400, { error: 'Bad Request', message: 'invalid session timeout: 1 minutes is outside 5-1440', code: 400 })
    )

    expect(err).toBeInstanceOf(ApiException)
    expect(err.status).toBe(400)
    expect(err.message).toBe('invalid session timeout: 1 minutes is outside 5-1440')
  })

  it('falls back to the status text for a body that is not JSON', async () => {
    const err = await responseError(new Response('upstream exploded', { status: 502, statusText: 'Bad Gateway' }))

    expect(err.status).toBe(502)
    expect(err.message).toBe('Bad Gateway')
  })
})

describe('desktop app API', () => {
  it('reads the raw menu text with session credentials and supports cancellation', async () => {
    fetchMock.mockResolvedValue(new Response('firefox.desktop:Name=Firefox', { status: 200 }))
    const controller = new AbortController()

    await expect(getQubeAppMenus('qube/one', controller.signal)).resolves.toBe('firefox.desktop:Name=Firefox')

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/qubes/qube%2Fone/appmenus')
    expect(init.credentials).toBe('include')
    expect(init.signal).toBe(controller.signal)
  })

  it('posts one encoded app id with no body and returns the remote reply', async () => {
    fetchMock.mockResolvedValue(new Response("qubes.StartApp: launched 'firefox.desktop' on :100", { status: 200 }))

    await expect(launchQubeApp('q1', 'firefox.desktop')).resolves.toMatch(/launched/)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/qubes/q1/apps/firefox.desktop/launch')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
    expect(init.body).toBeUndefined()
  })

  it('encodes the app id so it cannot add path segments', async () => {
    fetchMock.mockResolvedValue(new Response('', { status: 200 }))

    await launchQubeApp('q1', 'a/b?c#d')

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/qubes/q1/apps/a%2Fb%3Fc%23d/launch')
  })

  it('surfaces the refusal reason, not the status text', async () => {
    fetchMock.mockResolvedValue(jsonResponse(400, {
      error: 'Bad Request',
      message: 'invalid app id: app id must match [A-Za-z0-9._+-] and be at most 128 characters',
    }))

    await expect(launchQubeApp('q1', 'x')).rejects.toThrow(/app id must match/)
  })

  it('falls back to the status text for a transport failure without a reason', async () => {
    fetchMock.mockResolvedValue(jsonResponse(502, { error: 'Bad Gateway' }))

    await expect(getQubeAppMenus('q1')).rejects.toMatchObject({ status: 502, message: 'Bad Gateway' })
  })

  it('raises the auth gate when the session has expired', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { error: 'Unauthorized' }))

    await expect(getQubeAppMenus('q1')).rejects.toThrow()
    expect(auth.required).toBe(true)
  })
})

describe('desktop consent', () => {
  it('reads the approval queue with the session cookie', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { requests: [{ id: 'request-1' }] }))

    await expect(listDesktopAccessRequests()).resolves.toEqual([{ id: 'request-1' }])
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/desktop-access')
    expect(init.credentials).toBe('include')
  })

  it.each([
    [approveDesktopAccessRequest, 'approve'],
    [denyDesktopAccessRequest, 'deny'],
  ] as const)('sends %s with the console action header and an encoded id', async (action, path) => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: 'request/one', state: 'approved' }))

    await expect(action('request/one')).resolves.toEqual({ id: 'request/one', state: 'approved' })
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(`/api/v1/desktop-access/request%2Fone/${path}`)
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
    expect(init.headers).toMatchObject({ 'X-Console-Action': 'desktop-consent' })
    expect(init.body).toBeUndefined()
  })

  it('stops a grant with the same header', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))

    await stopDesktopAccessRequest('request-1')
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/desktop-access/request-1/stop')
    expect(init.method).toBe('POST')
    expect(init.headers).toMatchObject({ 'X-Console-Action': 'desktop-consent' })
  })

  it("surfaces the server's reason for a refused decision", async () => {
    fetchMock.mockResolvedValue(jsonResponse(403, {
      error: 'Forbidden', message: 'desktop approvals require a fleet-wide control session in the Console', code: 403,
    }))
    await expect(approveDesktopAccessRequest('request-1')).rejects.toMatchObject({
      status: 403, message: 'desktop approvals require a fleet-wide control session in the Console',
    })

    fetchMock.mockResolvedValue(jsonResponse(409, { error: 'Conflict', message: 'desktop access was stopped', code: 409 }))
    await expect(stopDesktopAccessRequest('request-1')).rejects.toBeInstanceOf(ApiException)
  })

  it('raises the sign-in gate when the session has expired', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { error: 'Unauthorized', code: 401 }))

    await expect(denyDesktopAccessRequest('request-1')).rejects.toMatchObject({ status: 401 })
    expect(auth.required).toBe(true)
  })
})
