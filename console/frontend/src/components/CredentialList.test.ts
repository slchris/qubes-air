import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import CredentialList from './CredentialList.svelte'
import * as api from '../lib/api'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, apiFetch: vi.fn() }
})

const apiFetch = vi.mocked(api.apiFetch)

interface CredentialFixture {
  id: string
  name: string
  type: string
  description: string
  lastUsed: string | null
  createdAt: string
}

function credentialFixture(overrides: Partial<CredentialFixture> = {}): CredentialFixture {
  return {
    id: 'c1',
    name: 'pve-root',
    type: 'proxmox',
    description: 'lab cluster',
    lastUsed: null,
    createdAt: '2026-09-20T00:00:00Z',
    ...overrides,
  }
}

function jsonResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: async () => body } as unknown as Response
}

function credentialsResponse(...credentials: CredentialFixture[]): Response {
  return jsonResponse({ credentials })
}

// Every test starts from a list that loaded. The delete and create paths re-ask
// for the list afterwards, so the default implementation answers by method
// rather than by call order.
function respondWith(overrides: { delete?: () => Promise<Response>; post?: () => Promise<Response> } = {}) {
  apiFetch.mockImplementation(async (_path: string, init?: RequestInit) => {
    if (init?.method === 'DELETE') {
      return overrides.delete ? overrides.delete() : jsonResponse({})
    }
    if (init?.method === 'POST' || init?.method === 'PUT') {
      return overrides.post ? overrides.post() : jsonResponse({})
    }
    return credentialsResponse(credentialFixture())
  })
}

beforeEach(() => {
  apiFetch.mockReset()
  respondWith()
})

describe('CredentialList rendering', () => {
  it('labels the credential type and says when it was never used', async () => {
    // "proxmox" is the provider this deployment actually uses; showing the raw
    // type string was how a PVE credential looked unconfigured.
    render(CredentialList)

    expect(await screen.findByText('pve-root')).toBeInTheDocument()
    expect(screen.getByText('Proxmox')).toBeInTheDocument()
    expect(screen.getByText('lab cluster')).toBeInTheDocument()
    expect(screen.getByText(/last used: never/i)).toBeInTheDocument()
  })

  it('states the empty case instead of rendering nothing', async () => {
    apiFetch.mockResolvedValue(credentialsResponse())

    render(CredentialList)

    expect(await screen.findByText(/no credentials configured/i)).toBeInTheDocument()
  })

  it('reports a failed load and recovers on retry', async () => {
    apiFetch.mockResolvedValueOnce(jsonResponse({}, 500))

    render(CredentialList)

    expect(await screen.findByText(/error: failed to load credentials/i)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /retry/i }))

    expect(await screen.findByText('pve-root')).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledTimes(2)
  })
})

describe('CredentialList delete', () => {
  it('sends nothing when the operator cancels the confirmation', async () => {
    // Negative path: a cancelled dialog must not reach the API at all — a delete
    // that only "looks" blocked because the request is in flight is not blocked.
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const alert = vi.spyOn(window, 'alert').mockImplementation(() => undefined)

    render(CredentialList)
    await screen.findByText('pve-root')
    await userEvent.click(screen.getByTitle('Delete'))

    expect(window.confirm).toHaveBeenCalledTimes(1)
    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(apiFetch).not.toHaveBeenCalledWith(expect.stringContaining('/credentials/c1'), expect.anything())
    expect(alert).not.toHaveBeenCalled()
    expect(screen.getByText('pve-root')).toBeInTheDocument()
  })

  it('surfaces a refused delete instead of dropping the card', async () => {
    // Negative path: the backend refuses (purge protection, in-use credential).
    // The list must keep showing the credential, because it still exists.
    respondWith({ delete: async () => jsonResponse({}, 409) })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const alert = vi.spyOn(window, 'alert').mockImplementation(() => undefined)

    render(CredentialList)
    await screen.findByText('pve-root')
    await userEvent.click(screen.getByTitle('Delete'))

    expect(alert).toHaveBeenCalledWith('Failed to delete')
    expect(screen.getByText('pve-root')).toBeInTheDocument()
  })

  it('deletes and refreshes the list once the confirmation is accepted', async () => {
    respondWith({ delete: async () => jsonResponse({}) })
    vi.spyOn(window, 'confirm').mockReturnValue(true)

    render(CredentialList)
    await screen.findByText('pve-root')
    await userEvent.click(screen.getByTitle('Delete'))

    expect(apiFetch).toHaveBeenCalledWith('/credentials/c1', { method: 'DELETE' })
    // Once to load, once to delete, once to refresh.
    expect(apiFetch).toHaveBeenCalledTimes(3)
  })
})

describe('CredentialList create', () => {
  it('refuses to submit without a secret before calling the API', async () => {
    render(CredentialList)
    await screen.findByText('pve-root')
    await userEvent.click(screen.getByRole('button', { name: /\+ add credential/i }))

    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))
    expect(await screen.findByText(/name is required/i)).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText(/^name$/i), 'prod-pve')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))
    expect(await screen.findByText(/secret is required/i)).toBeInTheDocument()

    // Only the initial list load: neither rejection reached the API.
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })

  it('posts to the API-relative path and closes the dialog on success', async () => {
    // The path matters: apiFetch already prepends /api/v1, so an absolute
    // /api/v1/... here produced /api/v1/api/v1/... and a 404 on every create.
    render(CredentialList)
    await screen.findByText('pve-root')
    await userEvent.click(screen.getByRole('button', { name: /\+ add credential/i }))

    await userEvent.type(screen.getByLabelText(/^name$/i), 'prod-pve')
    await userEvent.selectOptions(screen.getByLabelText(/^type$/i), 'proxmox')
    await userEvent.type(screen.getByLabelText(/^secret$/i), 'super-secret')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))

    const create = apiFetch.mock.calls.find(([, init]) => init?.method === 'POST')
    expect(create?.[0]).toBe('/credentials')
    expect(JSON.parse(String(create?.[1]?.body))).toEqual({
      name: 'prod-pve',
      description: '',
      type: 'proxmox',
      secret: 'super-secret',
    })
    expect(screen.queryByRole('dialog', { name: /add credential/i })).not.toBeInTheDocument()
  })

  it('keeps the dialog open and shows the backend refusal', async () => {
    respondWith({ post: async () => jsonResponse({ error: 'credential name already exists' }, 409) })

    render(CredentialList)
    await screen.findByText('pve-root')
    await userEvent.click(screen.getByRole('button', { name: /\+ add credential/i }))

    await userEvent.type(screen.getByLabelText(/^name$/i), 'pve-root')
    await userEvent.type(screen.getByLabelText(/^secret$/i), 'super-secret')
    await userEvent.click(screen.getByRole('button', { name: /^create$/i }))

    expect(await screen.findByText(/credential name already exists/i)).toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: /add credential/i })).toBeInTheDocument()
  })
})
