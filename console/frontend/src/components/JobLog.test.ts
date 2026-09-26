import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'

import JobLog from './JobLog.svelte'
import QubeList from './QubeList.svelte'
import { auth } from '../lib/auth.svelte'
import type { Qube } from '../lib/types'

// JobLog is driven through a fake fetch rather than a mocked api module, so the
// real streamJobLog/getJobLog run: the abort signal the component hands over is
// the one fetch sees, and SSE records are parsed exactly as in the browser.
//
// The fake speaks the backend contract (internal/handler/job_handler.go):
//   - GET /jobs/:id/log/stream?offset=N is text/event-stream; each record is
//     "data: <json>\n\n" with {offset,data,running} for output, {offset,error}
//     for a failed read, and a terminal {offset,data:"",running:false,state}
//     when the job ends. At its duration cap the stream simply closes.
//   - GET /jobs/:id/log?offset=N is the offset poller, same JSON shape.
//   - GET /jobs/:id is the job record, used when no output was recorded.

interface FakeStream {
  jobId: string
  offset: number
  signal: AbortSignal
  send(event: Record<string, unknown>): void
  end(): void
  drop(): void
}

interface Deferred<T> {
  promise: Promise<T>
  resolve(value: T): void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => (resolve = r))
  return { promise, resolve }
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function abortError(): DOMException {
  return new DOMException('The operation was aborted.', 'AbortError')
}

const encoder = new TextEncoder()

function openStream(jobId: string, offset: number, signal: AbortSignal): { stream: FakeStream; response: Response } {
  let ctrl!: ReadableStreamDefaultController<Uint8Array>
  let closed = false
  const body = new ReadableStream<Uint8Array>({ start: (c) => { ctrl = c } })
  const close = (how: () => void): void => {
    if (closed) return
    closed = true
    how()
  }
  // A real fetch fails the body read with an AbortError once its signal aborts.
  signal.addEventListener('abort', () => close(() => ctrl.error(abortError())))
  const stream: FakeStream = {
    jobId,
    offset,
    signal,
    send: (event) => {
      if (!closed) ctrl.enqueue(encoder.encode(`data: ${JSON.stringify(event)}\n\n`))
    },
    end: () => close(() => ctrl.close()),
    drop: () => close(() => ctrl.error(new TypeError('network error'))),
  }
  const response = new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
  return { stream, response }
}

type Reply = Response | Promise<Response>

class FakeServer {
  streams: FakeStream[] = []
  polls: Array<{ jobId: string; offset: number }> = []
  jobReads: string[] = []
  // Per-test behaviour. The defaults fail loudly so an unexpected request shows.
  streamReply: ((jobId: string) => Reply | null) | null = null
  // Any other GET, by pathname (the qube list, zones, job history, one qube).
  routes: Record<string, () => Reply> = {}
  hits: Record<string, number> = {}
  pollReply: (jobId: string, offset: number) => Reply = () => json(500, { error: 'unexpected poll' })
  jobReply: (jobId: string) => Reply = () => json(500, { error: 'unexpected job read' })

  fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(String(input), 'http://console.test')
    const signal = init?.signal ?? new AbortController().signal
    if (signal.aborted) return Promise.reject(abortError())

    const streamPath = url.pathname.match(/^\/api\/v1\/jobs\/([^/]+)\/log\/stream$/)
    const logPath = url.pathname.match(/^\/api\/v1\/jobs\/([^/]+)\/log$/)
    const jobPath = url.pathname.match(/^\/api\/v1\/jobs\/([^/]+)$/)
    const offset = Number(url.searchParams.get('offset'))

    if (streamPath) {
      const override = this.streamReply?.(streamPath[1])
      if (override) return Promise.resolve(override)
      const { stream, response } = openStream(streamPath[1], offset, signal)
      this.streams.push(stream)
      return Promise.resolve(response)
    }
    if (logPath) {
      this.polls.push({ jobId: logPath[1], offset })
      return Promise.resolve(this.pollReply(logPath[1], offset))
    }
    if (jobPath) {
      this.jobReads.push(jobPath[1])
      return Promise.resolve(this.jobReply(jobPath[1]))
    }
    const route = this.routes[url.pathname]
    if (route) {
      this.hits[url.pathname] = (this.hits[url.pathname] ?? 0) + 1
      return Promise.resolve(route())
    }
    return Promise.reject(new Error(`unexpected fetch ${url.pathname}`))
  }
}

let server: FakeServer

beforeEach(() => {
  auth.rejected = false
  server = new FakeServer()
  vi.stubGlobal('fetch', vi.fn(server.fetch))
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

// The real setTimeout, captured before any test installs fake timers.
const realSetTimeout = globalThis.setTimeout

// Macrotask turns: let the fake fetch resolve, the SSE body be read and parsed,
// and Svelte flush the resulting DOM update. Uses the real timer, so it also
// works while the component's own setTimeout is faked.
async function flush(turns = 3): Promise<void> {
  for (let i = 0; i < turns; i++) await new Promise((r) => realSetTimeout(r, 0))
}

function logText(container: HTMLElement): string {
  return container.querySelector('pre')?.textContent ?? ''
}

function mount(jobId: string, active: boolean) {
  return render(JobLog, { props: { jobId, active } })
}

describe('JobLog live stream', () => {
  it('keeps the stream attached after mounting a running job and shows its output', async () => {
    const { container } = mount('j1', true)
    await flush()

    expect(server.streams).toHaveLength(1)
    const [stream] = server.streams
    expect(stream).toMatchObject({ jobId: 'j1', offset: 0 })
    // The regression: the mount effect re-ran on its own writes and its
    // teardown aborted the stream it had just opened, so a running job never
    // showed a line of output.
    expect(stream.signal.aborted).toBe(false)
    expect(screen.getByText('Waiting for job output…')).toBeInTheDocument()

    stream.send({ offset: 6, data: 'hello\n', running: true })
    await flush()

    expect(stream.signal.aborted).toBe(false)
    expect(logText(container)).toBe('hello\n')
    expect(screen.getByText(/live stream/)).toBeInTheDocument()
    expect(server.streams).toHaveLength(1)
    expect(server.polls).toHaveLength(0)
  })

  it('does not restart the feed when the parent re-renders with the same job', async () => {
    const { container, rerender } = mount('j1', true)
    await flush()
    server.streams[0].send({ offset: 6, data: 'hello\n', running: true })
    await flush()

    // What a qube-list refresh does: the prop expressions are re-evaluated and
    // yield the same values.
    await rerender({ jobId: 'j1', active: true })
    await flush()

    expect(server.streams).toHaveLength(1)
    expect(server.streams[0].signal.aborted).toBe(false)
    expect(logText(container)).toBe('hello\n')
  })

  it('does not tear the stream down when the active prop changes mid-job', async () => {
    const { rerender } = mount('j1', true)
    await flush()

    await rerender({ jobId: 'j1', active: false })
    await flush()

    expect(server.streams).toHaveLength(1)
    expect(server.streams[0].signal.aborted).toBe(false)
  })

  it('aborts the old stream and starts a fresh one when the job changes', async () => {
    const { container, rerender } = mount('j1', true)
    await flush()
    const [first] = server.streams
    first.send({ offset: 4, data: 'one\n', running: true })
    await flush()
    expect(logText(container)).toBe('one\n')

    await rerender({ jobId: 'j2', active: true })
    await flush()

    expect(first.signal.aborted).toBe(true)
    expect(server.streams).toHaveLength(2)
    const second = server.streams[1]
    // A new job starts from its own beginning, not the previous job's offset.
    expect(second).toMatchObject({ jobId: 'j2', offset: 0 })
    expect(second.signal.aborted).toBe(false)
    expect(logText(container)).toBe('')

    second.send({ offset: 4, data: 'two\n', running: true })
    await flush()
    expect(logText(container)).toBe('two\n')
  })

  it('does not carry the previous job\'s failure over to the next job', async () => {
    server.pollReply = () => json(200, { offset: 5, data: 'boom\n', running: false, state: 'failed' })
    const { container, rerender } = mount('j1', false)
    await flush()
    expect(container.querySelector('.joblog')).toHaveClass('failed')

    await rerender({ jobId: 'j2', active: true })
    await flush()

    expect(container.querySelector('.joblog')).not.toHaveClass('failed')
    expect(screen.getByText('Waiting for job output…')).toBeInTheDocument()
  })

  it('drops a late poll answer for a job that is no longer shown', async () => {
    const late = deferred<Response>()
    server.pollReply = (jobId) => (jobId === 'j1' ? late.promise : json(500, {}))
    const { container, rerender } = mount('j1', false)
    await flush()
    expect(server.polls).toEqual([{ jobId: 'j1', offset: 0 }])

    await rerender({ jobId: 'j2', active: true })
    await flush()
    server.streams[0].send({ offset: 4, data: 'new\n', running: true })
    await flush()

    late.resolve(json(200, { offset: 4, data: 'old\n', running: false, state: 'succeeded' }))
    await flush()

    expect(logText(container)).toBe('new\n')
    expect(server.streams[0].signal.aborted).toBe(false)
    expect(server.jobReads).toHaveLength(0)
  })

  it('drops a late job-record answer for a job that is no longer shown', async () => {
    // j1 finished with an empty log, so finish() asks for the job record; that
    // answer arrives only after the panel has moved on to j2.
    const late = deferred<Response>()
    server.pollReply = (jobId) =>
      jobId === 'j1' ? json(200, { offset: 0, data: '', running: false, state: 'failed' }) : json(500, {})
    server.jobReply = (jobId) => (jobId === 'j1' ? late.promise : json(500, {}))
    const { container, rerender } = mount('j1', false)
    await flush()
    expect(server.jobReads).toEqual(['j1'])

    await rerender({ jobId: 'j2', active: true })
    await flush()
    server.streams[0].send({ offset: 4, data: 'new\n', running: true })
    await flush()

    late.resolve(json(200, { id: 'j1', state: 'failed', error: 'old failure' }))
    await flush()

    expect(logText(container)).toBe('new\n')
    expect(screen.queryByText(/old failure/)).not.toBeInTheDocument()
    expect(container.querySelector('.joblog')).not.toHaveClass('failed')
  })

  it('aborts the stream on unmount and does not reconnect', async () => {
    const { unmount } = mount('j1', true)
    await flush()
    const [stream] = server.streams

    unmount()
    await flush()

    expect(stream.signal.aborted).toBe(true)
    expect(server.streams).toHaveLength(1)
    expect(server.polls).toHaveLength(0)
  })

  it('does not stream a job that had already finished; it reads the log once', async () => {
    server.pollReply = () => json(200, { offset: 8, data: 'all ok\n', running: false, state: 'succeeded' })
    const { container } = mount('j1', false)
    await flush()

    expect(server.streams).toHaveLength(0)
    expect(server.polls).toEqual([{ jobId: 'j1', offset: 0 }])
    expect(server.jobReads).toHaveLength(0)

    // Finished jobs start collapsed; the output is there when opened.
    await userEvent.click(screen.getByRole('button', { name: /job log/i }))
    expect(logText(container)).toBe('all ok\n')
  })

  it('falls back to the job error when a finished job recorded no output', async () => {
    server.pollReply = () => json(200, { offset: 0, data: '', running: false, state: 'failed' })
    server.jobReply = (id) => json(200, { id, state: 'failed', error: 'provider refused the request' })
    const { container } = mount('j1', false)
    await flush()

    expect(server.streams).toHaveLength(0)
    expect(server.jobReads).toEqual(['j1'])
    expect(logText(container)).toBe('provider refused the request')
    expect(screen.getByText(/failed — see log/i)).toBeInTheDocument()
  })
})

// The server's duration cap (streamMaxDuration in job_handler.go).
const STREAM_CAP_MS = 5 * 60_000

// Ends a stream the way the server does at its cap: the clock has moved on by
// the cap and the connection closes without a terminal event.
async function endAtCap(stream: FakeStream): Promise<void> {
  vi.setSystemTime(Date.now() + STREAM_CAP_MS)
  stream.end()
  await flush()
}

describe('JobLog stream contract', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  })

  it('reconnects from the last offset when the stream ends at its cap', async () => {
    const { container } = mount('j1', true)
    await flush()
    server.streams[0].send({ offset: 6, data: 'hello\n', running: true })
    await flush()
    await endAtCap(server.streams[0])

    expect(server.streams).toHaveLength(2)
    expect(server.streams[1]).toMatchObject({ jobId: 'j1', offset: 6 })
    expect(server.streams[1].signal.aborted).toBe(false)

    server.streams[1].send({ offset: 12, data: 'world\n', running: true })
    await flush()
    expect(logText(container)).toBe('hello\nworld\n')
  })

  it('stops after the terminal event without polling or reconnecting', async () => {
    const { container } = mount('j1', true)
    await flush()
    const [stream] = server.streams
    stream.send({ offset: 5, data: 'done\n', running: true })
    stream.send({ offset: 5, data: '', running: false, state: 'succeeded' })
    stream.end()
    await flush()

    expect(server.streams).toHaveLength(1)
    expect(server.polls).toHaveLength(0)
    expect(server.jobReads).toHaveLength(0)
    expect(logText(container)).toBe('done\n')
    expect(screen.getByText('Job log')).toBeInTheDocument()
  })

  it('keeps streaming through a read-error event, which is not the end of the job', async () => {
    const { container } = mount('j1', true)
    await flush()
    server.streams[0].send({ offset: 6, data: 'hello\n', running: true })
    server.streams[0].send({ offset: 6, error: 'read log: disk busy' })
    await flush()

    expect(screen.getByText('read log: disk busy')).toBeInTheDocument()
    expect(screen.getByText(/live stream/)).toBeInTheDocument()

    await endAtCap(server.streams[0])
    // Still running, so the cap is followed by a reconnect at the same offset.
    expect(server.streams).toHaveLength(2)
    expect(server.streams[1].offset).toBe(6)

    server.streams[1].send({ offset: 12, data: 'world\n', running: true })
    await flush()
    expect(screen.queryByText('read log: disk busy')).not.toBeInTheDocument()
    expect(logText(container)).toBe('hello\nworld\n')
  })

  it('degrades to the offset poller from the last offset when the stream drops', async () => {
    server.pollReply = () => json(200, { offset: 11, data: 'tail\n', running: false, state: 'failed' })
    const { container } = mount('j1', true)
    await flush()
    server.streams[0].send({ offset: 6, data: 'hello\n', running: true })
    server.streams[0].drop()
    await flush()

    expect(server.streams).toHaveLength(1)
    expect(server.polls).toEqual([{ jobId: 'j1', offset: 6 }])
    expect(logText(container)).toBe('hello\ntail\n')
    expect(screen.getByText(/failed — see log/i)).toBeInTheDocument()
  })

  it('polls instead when the stream endpoint is unavailable', async () => {
    server.streamReply = () => json(500, { error: 'streaming unsupported' })
    server.pollReply = () => json(200, { offset: 3, data: 'ok\n', running: false, state: 'succeeded' })
    const { container } = mount('j1', true)
    await flush()

    expect(server.streams).toHaveLength(0)
    expect(server.polls).toEqual([{ jobId: 'j1', offset: 0 }])
    expect(logText(container)).toBe('ok\n')
  })

  it('shows a poll failure and stops polling', async () => {
    server.streamReply = () => json(500, { error: 'streaming unsupported' })
    server.pollReply = () => json(404, { error: 'Job not found' })
    server.jobReply = () => json(404, { error: 'Job not found' })
    mount('j1', true)
    await flush()

    expect(screen.getByText('Job not found')).toBeInTheDocument()
    expect(server.polls).toHaveLength(1)
    expect(screen.queryByText(/live/)).not.toBeInTheDocument()
  })
})

describe('JobLog pacing and teardown', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  })

  it('keeps polling on its interval while the job runs, from the returned offset', async () => {
    server.streamReply = () => json(500, { error: 'streaming unsupported' })
    let n = 0
    server.pollReply = (_id, offset) => {
      n++
      const running = n < 3
      return json(200, { offset: offset + 2, data: `${n}\n`, running, state: running ? 'running' : 'succeeded' })
    }
    const { container } = mount('j1', true)
    await flush()
    expect(server.polls).toEqual([{ jobId: 'j1', offset: 0 }])

    await vi.advanceTimersByTimeAsync(2999)
    await flush()
    expect(server.polls).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(1)
    await flush()
    await vi.advanceTimersByTimeAsync(3000)
    await flush()
    expect(server.polls.map((p) => p.offset)).toEqual([0, 2, 4])
    expect(logText(container)).toBe('1\n2\n3\n')

    // Finished: no further polls however long the panel stays open.
    await vi.advanceTimersByTimeAsync(30_000)
    await flush()
    expect(server.polls).toHaveLength(3)
  })

  it('stops the poll loop when the panel unmounts', async () => {
    server.streamReply = () => json(500, { error: 'streaming unsupported' })
    server.pollReply = (_id, offset) => json(200, { offset, data: '', running: true, state: 'running' })
    const { unmount } = mount('j1', true)
    await flush()
    expect(server.polls).toHaveLength(1)

    unmount()
    await vi.advanceTimersByTimeAsync(30_000)
    await flush()

    expect(server.polls).toHaveLength(1)
    expect(server.jobReads).toHaveLength(0)
  })

  it('stops the old poll loop when the job changes', async () => {
    server.streamReply = () => json(500, { error: 'streaming unsupported' })
    server.pollReply = (_id, offset) => json(200, { offset, data: '', running: true, state: 'running' })
    const { rerender } = mount('j1', true)
    await flush()

    await rerender({ jobId: 'j2', active: true })
    await flush()
    await vi.advanceTimersByTimeAsync(9000)
    await flush()

    const j1 = server.polls.filter((p) => p.jobId === 'j1')
    const j2 = server.polls.filter((p) => p.jobId === 'j2')
    expect(j1).toHaveLength(1)
    expect(j2.length).toBeGreaterThanOrEqual(3)
  })

  it('does not reconnect in a tight loop when a stream closes at once', async () => {
    mount('j1', true)
    await flush()
    server.streams[0].end()
    await flush()
    // A stream that closed immediately (a proxy, not the 5-minute cap) is not
    // re-opened until the minimum reconnect interval has passed.
    expect(server.streams).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(2000)
    await flush()
    expect(server.streams).toHaveLength(2)
    expect(server.streams[1].offset).toBe(0)
  })

  it('reconnects at once after a stream that ran for its full cap', async () => {
    mount('j1', true)
    await flush()
    server.streams[0].send({ offset: 3, data: 'hi\n', running: true })
    await flush()
    await endAtCap(server.streams[0])

    expect(server.streams).toHaveLength(2)
    expect(server.streams[1].offset).toBe(3)
  })

  it('stops pacing a reconnect when the panel unmounts during the wait', async () => {
    const { unmount } = mount('j1', true)
    await flush()
    server.streams[0].end()
    await flush()

    unmount()
    await vi.advanceTimersByTimeAsync(10_000)
    await flush()

    expect(server.streams).toHaveLength(1)
    expect(server.polls).toHaveLength(0)
  })
})

describe('JobLog inside the qube list', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
  })

  it('keeps the card stream attached while the qube list refreshes around it', async () => {
    const creating: Qube = {
      purge_requested: false,
      id: 'q1',
      name: 'qube-one',
      type: 'dev',
      status: 'creating',
      spec: { vcpu: 1, memory: 512, disk: 20 },
      created_at: '2026-09-21T00:00:00Z',
      updated_at: '2026-09-21T00:00:00Z',
    }
    let current = creating
    server.routes['/api/v1/qubes'] = () => json(200, { qubes: [current], total: 1 })
    server.routes['/api/v1/qubes/q1'] = () => json(200, current)
    server.routes['/api/v1/zones'] = () => json(200, { zones: [], total: 0 })
    server.routes['/api/v1/jobs'] = () =>
      json(200, { jobs: [{ id: 'j1', qube_id: 'q1', action: 'provision', state: 'running' }], total: 1 })

    const { container } = render(QubeList)
    await flush(6)

    expect(server.streams).toHaveLength(1)
    const [stream] = server.streams
    expect(stream).toMatchObject({ jobId: 'j1', offset: 0 })
    stream.send({ offset: 6, data: 'hello\n', running: true })
    await flush()

    // The store re-reads a transient qube every 3 s and replaces the list,
    // which re-evaluates the card's jobId expression each time.
    for (let i = 0; i < 2; i++) {
      await vi.advanceTimersByTimeAsync(3000)
      await flush()
    }
    expect(server.hits['/api/v1/qubes/q1']).toBe(2)
    expect(stream.signal.aborted).toBe(false)
    expect(server.streams).toHaveLength(1)
    expect(logText(container)).toBe('hello\n')

    // The qube settles, so the card's active prop flips; the stream still runs
    // until the job's own terminal event.
    current = { ...creating, status: 'running' }
    await vi.advanceTimersByTimeAsync(3000)
    await flush()
    expect(stream.signal.aborted).toBe(false)

    stream.send({ offset: 6, data: '', running: false, state: 'succeeded' })
    stream.end()
    await flush()
    expect(server.streams).toHaveLength(1)
    expect(logText(container)).toBe('hello\n')
  })
})
