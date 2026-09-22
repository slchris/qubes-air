import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'

import Header from './Header.svelte'
import * as api from '../lib/api'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, getHealth: vi.fn() }
})

const getHealth = vi.mocked(api.getHealth)

// The header reads exactly one field; the rest of the body is the server's
// business (cmd/server/health_test.go pins that shape on the Go side).
function healthFixture(version: string): Awaited<ReturnType<typeof api.getHealth>> {
  return {
    status: 'healthy',
    database: 'connected',
    worker: { dispatcher: 'running', queued: 0, running: 0 },
    version,
    revision: '0123456789abcdef0123456789abcdef01234567',
    build_time: '2026-09-22T12:33:55Z',
    tree: 'clean',
  }
}

// The version arrives after mount. A negative assertion ("no version is shown")
// evaluated before that answer lands proves nothing — it would also pass on a
// component that renders a constant a moment later. Waiting a macrotask means
// every pending microtask of the fetch has run, so "nothing is rendered" is
// then a statement about the settled component.
async function settled(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
}

beforeEach(() => {
  getHealth.mockReset()
})

describe('Header build identity', () => {
  it('shows the version the server reports, not a constant of its own', async () => {
    getHealth.mockResolvedValue(healthFixture('v1.2.3-4-gabcdef-dirty'))

    render(Header)

    expect(await screen.findByText('v1.2.3-4-gabcdef-dirty')).toBeInTheDocument()
    // The regression this guards: the header used to carry its own "0.1.0",
    // which is a release number no build ever produced.
    expect(screen.queryByText(/0\.1\.0/)).toBeNull()
  })

  it('shows no version when the server reports an unstamped build', async () => {
    getHealth.mockResolvedValue(healthFixture('unknown'))

    const { container } = render(Header)

    expect(await screen.findByText('Qubes Air')).toBeInTheDocument()
    await settled()
    // No version-shaped text in the header: neither a stamp nor a constant.
    expect(container.querySelector('.version')?.textContent ?? '').toBe('')
    expect(screen.queryByText(/0\.1\.0/)).toBeNull()
  })

  it('shows no version when the console cannot be reached', async () => {
    getHealth.mockRejectedValue(new Error('Failed to fetch'))

    const { container } = render(Header)

    expect(await screen.findByText('Qubes Air')).toBeInTheDocument()
    await settled()
    // No version-shaped text in the header: neither a stamp nor a constant.
    expect(container.querySelector('.version')?.textContent ?? '').toBe('')
    expect(screen.queryByText(/0\.1\.0/)).toBeNull()
  })
})
