import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import AppMenuDialog from './AppMenuDialog.svelte'
import * as api from '../lib/api'

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return {
    ...actual,
    getQubeAppMenus: vi.fn(),
    launchQubeApp: vi.fn(),
  }
})

const getQubeAppMenus = vi.mocked(api.getQubeAppMenus)
const launchQubeApp = vi.mocked(api.launchQubeApp)

// A request that only ends when its signal aborts, like fetch does.
function pendingUntilAborted(signal?: AbortSignal): Promise<string> {
  return new Promise((_resolve, reject) => {
    signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))
  })
}

beforeEach(() => {
  getQubeAppMenus.mockReset()
  launchQubeApp.mockReset()
})

afterEach(() => vi.restoreAllMocks())

describe('AppMenuDialog', () => {
  it('loads named apps and launches using the validated desktop id', async () => {
    getQubeAppMenus.mockResolvedValue([
      'firefox.desktop:Name=Firefox',
      'firefox.desktop:Exec=qubes-desktop-run firefox.desktop',
      'bad/id.desktop:Name=Injected',
    ].join('\n'))
    launchQubeApp.mockResolvedValue("qubes.StartApp: launched 'firefox.desktop' on :100")
    const onclose = vi.fn()

    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose } })

    expect(screen.getByRole('dialog', { name: 'Applications' })).toBeInTheDocument()
    const launchButton = await screen.findByRole('button', { name: 'Launch Firefox' })
    expect(screen.getByText('Firefox')).toBeInTheDocument()
    expect(screen.queryByText('Injected')).not.toBeInTheDocument()
    expect(getQubeAppMenus).toHaveBeenCalledWith('q1', expect.any(AbortSignal))
    await userEvent.click(launchButton)

    expect(launchQubeApp).toHaveBeenCalledTimes(1)
    expect(launchQubeApp).toHaveBeenCalledWith('q1', 'firefox.desktop', expect.any(AbortSignal))
    const notice = await screen.findByRole('status')
    expect(notice).toHaveTextContent(/launch request sent for firefox/i)
    expect(notice).toHaveClass('success')
    expect(onclose).not.toHaveBeenCalled()
  })

  it('treats a refusal in the remote reply as a failed launch', async () => {
    // qubes.StartApp exits 0 on failure, so the endpoint answers 200 and the
    // failure is only in the reply text.
    getQubeAppMenus.mockResolvedValue('firefox.desktop:Name=Firefox')
    launchQubeApp.mockResolvedValue("qubes.StartApp: failed to launch 'firefox.desktop' on :100: no display")

    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })
    await userEvent.click(await screen.findByRole('button', { name: 'Launch Firefox' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/firefox did not start: .*no display/i)
    expect(screen.queryByText(/launch request sent/i)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Launch Firefox' })).toBeEnabled()
  })

  it('does not claim a launch for a reply it does not recognise', async () => {
    getQubeAppMenus.mockResolvedValue('firefox.desktop:Name=Firefox')
    launchQubeApp.mockResolvedValue('OK')

    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })
    await userEvent.click(await screen.findByRole('button', { name: 'Launch Firefox' }))

    const notice = await screen.findByRole('status')
    expect(notice).toHaveTextContent(/reply was not recognised.*whether firefox started: OK/i)
    expect(notice).not.toHaveClass('success')
    expect(screen.queryByText(/launch request sent/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows the backend reason when a launch is refused', async () => {
    getQubeAppMenus.mockResolvedValue('firefox.desktop:Name=Firefox')
    launchQubeApp.mockRejectedValue(new api.ApiException(503, 'UNKNOWN_ERROR', 'zone is not connected'))

    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })
    await userEvent.click(await screen.findByRole('button', { name: 'Launch Firefox' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('zone is not connected')
  })

  it('reports menu errors without offering launch actions', async () => {
    getQubeAppMenus.mockRejectedValue(new api.ApiException(502, 'UNREACHABLE', 'agent is offline'))
    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })

    expect(await screen.findByRole('alert')).toHaveTextContent('agent is offline')
    expect(screen.queryByRole('button', { name: /^launch/i })).not.toBeInTheDocument()
  })

  it('does not repeat a raw network error', async () => {
    getQubeAppMenus.mockRejectedValue(new TypeError('Failed to fetch'))
    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })

    expect(await screen.findByRole('alert')).toHaveTextContent('Could not read applications from this qube')
  })

  it('says so when the qube reports nothing launchable', async () => {
    getQubeAppMenus.mockResolvedValue('hidden.desktop:Exec=qubes-desktop-run hidden.desktop\n')
    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })

    expect(await screen.findByText(/no launchable applications/i)).toBeInTheDocument()
  })

  it('shows remote names as text, not markup', async () => {
    getQubeAppMenus.mockResolvedValue('x.desktop:Name=<img src=x onerror=alert(1)>')
    const { container } = render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })

    expect(await screen.findByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
  })

  it('allows one launch at a time', async () => {
    getQubeAppMenus.mockResolvedValue(['a.desktop:Name=Alpha', 'b.desktop:Name=Beta'].join('\n'))
    launchQubeApp.mockImplementation((_id, _app, signal) => pendingUntilAborted(signal))
    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })

    await userEvent.click(await screen.findByRole('button', { name: 'Launch Alpha' }))

    expect(await screen.findByRole('button', { name: 'Starting Alpha' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Launch Beta' })).toBeDisabled()
    expect(launchQubeApp).toHaveBeenCalledTimes(1)
  })

  it('cancels an in-flight menu request when closed', async () => {
    let requestSignal: AbortSignal | undefined
    getQubeAppMenus.mockImplementation((_id, signal) => {
      requestSignal = signal
      return pendingUntilAborted(signal)
    })
    const onclose = vi.fn()
    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose } })

    await screen.findByRole('status')
    await userEvent.click(screen.getByRole('button', { name: 'Close applications' }))

    expect(requestSignal?.aborted).toBe(true)
    expect(onclose).toHaveBeenCalledOnce()
  })

  it('cancels an in-flight launch when closed with Escape', async () => {
    let requestSignal: AbortSignal | undefined
    getQubeAppMenus.mockResolvedValue('firefox.desktop:Name=Firefox')
    launchQubeApp.mockImplementation((_id, _app, signal) => {
      requestSignal = signal
      return pendingUntilAborted(signal)
    })
    const onclose = vi.fn()
    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose } })

    await userEvent.click(await screen.findByRole('button', { name: 'Launch Firefox' }))
    expect(await screen.findByRole('button', { name: 'Starting Firefox' })).toBeDisabled()
    await userEvent.keyboard('{Escape}')

    expect(requestSignal?.aborted).toBe(true)
    expect(onclose).toHaveBeenCalledOnce()
  })

  it('cancels its requests when unmounted by the parent', async () => {
    let requestSignal: AbortSignal | undefined
    getQubeAppMenus.mockImplementation((_id, signal) => {
      requestSignal = signal
      return pendingUntilAborted(signal)
    })
    const { unmount } = render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })

    await screen.findByRole('status')
    unmount()

    expect(requestSignal?.aborted).toBe(true)
  })

  it('moves focus to the close button when it opens', () => {
    getQubeAppMenus.mockImplementation((_id, signal) => pendingUntilAborted(signal))
    render(AppMenuDialog, { props: { qubeId: 'q1', qubeName: 'work', onclose: vi.fn() } })

    expect(screen.getByRole('button', { name: 'Close applications' })).toHaveFocus()
  })
})
