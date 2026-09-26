<!--
  Qubes Air Console - live operation output for one job.

  A provision runs for many minutes. Before this, the card showed a disabled
  "Provisioning…" button for the whole time and, on failure, nothing but a red
  dot — the operation's error was returned by the API and thrown away. This tails
  the job's output while it runs and keeps the final output (including the error)
  once it ends.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { getJobLog, getJob, streamJobLog, type JobLogChunk } from '../lib/api';

  interface Props {
    jobId: string;
    // Whether the qube is still in a transient state. Used only as the initial
    // guess; the log endpoint's own `running` flag is authoritative once the
    // feed starts, because the job record is what knows it finished.
    active?: boolean;
  }
  let { jobId, active = true }: Props = $props();

  let text = $state('');
  let running = $state(false);
  let jobState = $state<string | null>(null);
  let error = $state<string | null>(null);
  let open = $state(false); // auto-expanded from active when a job is attached
  let streaming = $state(false); // true while the live stream is attached

  // Where the next read resumes. Not rendered, so deliberately not reactive.
  let offset = 0;

  let pre = $state<HTMLPreElement | null>(null);

  const POLL_MS = 3000;
  // A stream that closes sooner than this after opening (a proxy dropping it,
  // not the server's multi-minute cap) waits out the rest before reconnecting,
  // so a misbehaving hop cannot turn the reconnect loop into a request storm.
  // Measured on performance.now(), which is monotonic: a wall-clock step (NTP,
  // suspend/resume, a manual change) must not stretch or skip the wait.
  const RECONNECT_MIN_MS = 2000;
  // This many streams in a row that closed that quickly without delivering a
  // single event means the stream path is not working (a proxy that answers
  // 200 and hangs up, say), so the feed falls back to the offset poller —
  // which also learns from the job record whether the job is still running.
  // A quiet stream that stays open is not counted: a queued job can print
  // nothing for the whole server cap.
  const MAX_QUIET_SHORT_STREAMS = 3;

  // One feed per job id, restarted only when the id itself changes.
  //
  // The effect depends on feedJobId and NOTHING else. Everything the feed reads
  // and writes (running, text, …) happens under untrack: an effect that depends
  // on state it writes re-runs itself, and each re-run's teardown aborts the
  // stream the previous run just opened — which is how a running job used to
  // show "Waiting…" forever. `active` is read there too; it is only the initial
  // guess for a new job, and re-running on its change would abort a live stream
  // during a status transition.
  //
  // feedJobId is a $derived rather than a direct prop read because the parent's
  // prop expression (qubeState.jobs[qube.id]) is re-evaluated on every qube-list
  // refresh. A derived only notifies when the id's VALUE changes, so a refresh
  // that yields the same id leaves the running feed alone.
  //
  // Each feed owns its AbortSignal. Everything it does after an await checks
  // that signal instead of comparing ids, so a late answer for a job that is no
  // longer shown (or a panel that is gone) is dropped rather than applied.
  const feedJobId = $derived(jobId);
  $effect(() => {
    const id = feedJobId;
    if (!id) return;
    const feedController = new AbortController();
    untrack(() => start(id, feedController.signal));
    return () => feedController.abort();
  });

  function start(id: string, signal: AbortSignal): void {
    text = '';
    offset = 0;
    jobState = null;
    error = null;
    streaming = false;
    running = active;
    open = active;
    void feed(id, signal);
  }

  // Auto-scroll to the newest line as output arrives, but only when the view is
  // already at (or near) the bottom — so an operator who scrolled up to read
  // something is not yanked back down on every chunk.
  function apply(chunk: JobLogChunk, signal: AbortSignal): void {
    if (signal.aborted) return;
    const el = pre;
    const atBottom = el ? el.scrollHeight - el.scrollTop - el.clientHeight < 40 : true;
    if (chunk.data) text += chunk.data;
    offset = chunk.offset;
    // A read-error event carries an offset and an error but no running flag.
    // It is a failed read on the console, not the end of the job, so it must
    // not stop the feed; the next good chunk clears the message.
    if (typeof chunk.running === 'boolean') running = chunk.running;
    jobState = chunk.state ?? jobState;
    error = chunk.error ?? null;
    if (el && atBottom) queueMicrotask(() => { el.scrollTop = el.scrollHeight; });
  }

  // Stream first while the job runs, poll as the fallback. The stream gives
  // line-by-line output; when it ends at the server's duration cap the loop
  // reconnects from the last offset, and when it fails (older console, proxy,
  // a dropped qrexec forward) control falls through to polling from that same
  // offset, so nothing is missed or repeated.
  async function feed(id: string, signal: AbortSignal): Promise<void> {
    const fallBack = await streamWhileRunning(id, signal);
    if (signal.aborted) return;

    // A job that was ALREADY finished when this panel opened never entered the
    // stream loop, so its log has not been fetched. Do one plain read — the log
    // still exists on the server long after the job ended, and without this a
    // succeeded job shows an empty panel even though its full output is there.
    if (fallBack || !text) {
      await poll(id, signal);
      return;
    }
    await finish(id, signal);
  }

  // Streams while the job runs. Returns true when the stream path failed and
  // the caller should poll instead, false when the job is no longer running
  // (or the feed was torn down).
  async function streamWhileRunning(id: string, signal: AbortSignal): Promise<boolean> {
    let quietShort = 0;
    while (running) {
      const opened = performance.now();
      let events = 0;
      const onChunk = (chunk: JobLogChunk): void => {
        events++;
        apply(chunk, signal);
      };
      streaming = true;
      try {
        await streamJobLog(id, offset, onChunk, signal);
      } catch {
        if (!signal.aborted) streaming = false;
        return true;
      }
      if (signal.aborted) return false;
      streaming = false;
      const elapsed = performance.now() - opened;
      quietShort = events === 0 && elapsed < RECONNECT_MIN_MS ? quietShort + 1 : 0;
      if (quietShort >= MAX_QUIET_SHORT_STREAMS) return true;
      if (running) await wait(RECONNECT_MIN_MS - elapsed, signal);
      if (signal.aborted) return false;
    }
    return false;
  }

  async function poll(id: string, signal: AbortSignal): Promise<void> {
    for (;;) {
      try {
        apply(await getJobLog(id, offset), signal);
      } catch (e) {
        if (signal.aborted) return;
        error = e instanceof Error ? e.message : 'Failed to read job log';
        running = false;
      }
      if (signal.aborted) return;
      if (!running) break;
      await wait(POLL_MS, signal);
      if (signal.aborted) return;
    }
    await finish(id, signal);
  }

  // The job finished. If nothing was logged (logs disabled on this console),
  // fall back to the job's own error field so a failure still has a reason.
  async function finish(id: string, signal: AbortSignal): Promise<void> {
    if (signal.aborted || text.trim()) return;
    try {
      const job = await getJob(id);
      if (signal.aborted) return;
      jobState = job.state;
      if (job.error) { text = job.error; open = true; }
    } catch {
      // Leave the panel empty rather than surfacing a secondary error.
    }
  }

  // Resolves after ms, or as soon as the feed is torn down, so a torn-down feed
  // never leaves a timer behind that fires a request for a panel nobody sees.
  function wait(ms: number, signal: AbortSignal): Promise<void> {
    return new Promise((resolve) => {
      if (ms <= 0 || signal.aborted) {
        resolve();
        return;
      }
      const done = (): void => {
        clearTimeout(timer);
        signal.removeEventListener('abort', done);
        resolve();
      };
      const timer = setTimeout(done, ms);
      signal.addEventListener('abort', done, { once: true });
    });
  }

  // Strip ANSI colour codes some tools emit; they render as noise in HTML.
  const clean = $derived(text.replace(/\x1b\[[0-9;]*m/g, ''));
  const failed = $derived(jobState === 'failed');
</script>

<div class="joblog" class:failed>
  <button class="bar" onclick={() => (open = !open)}>
    <span class="chev">{open ? '▾' : '▸'}</span>
    <span class="title">
      {#if running}Provisioning — {streaming ? 'live stream' : 'live log'}{:else if failed}Failed — see log{:else}Job log{/if}
    </span>
    {#if running}<span class="spinner" title={streaming ? 'streaming' : 'polling'}>●</span>{/if}
  </button>

  {#if open}
    {#if error}
      <p class="err">{error}</p>
    {/if}
    {#if clean.trim()}
      <pre bind:this={pre}>{clean}</pre>
    {:else if running}
      <p class="waiting">Waiting for job output…</p>
    {:else}
      <p class="waiting">No output recorded for this job.</p>
    {/if}
  {/if}
</div>

<style>
  /* Explicit foreground next to every hard-coded background — the collapsed
     bar was light-on-light in dark mode for the same reason as the login gate. */
  .joblog {

    margin-top: 0.6rem; border: 1px solid var(--systemQuaternary); border-radius: var(--global-border-radius-xsmall);
    overflow: hidden;
  }

  .joblog.failed { border-color: var(--systemRed); }
  .bar {
    width: 100%; display: flex; align-items: center; gap: 0.5rem;
    padding: 0.4rem 0.6rem; background: var(--systemQuinary); color: var(--systemPrimary);
    border: none; cursor: pointer; font: var(--callout); text-align: left;
  }
  .joblog.failed .bar { background: #fef2f2; color: #991b1b; }
  .chev { width: 1em; }
  .title { flex: 1; }
  .spinner { color: var(--keyColor); animation: pulse 1.2s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.25; } }
  pre {
    margin: 0; padding: 0.6rem; max-height: 22rem; overflow: auto;
    background: #0f172a; color: #e2e8f0;
    font: var(--subhead); line-height: 1.45; white-space: pre-wrap; word-break: break-word;
  }
  .waiting, .err { margin: 0; padding: 0.6rem; font: var(--callout); color: var(--systemSecondary); }
  .err { color: var(--systemRed); }
</style>
