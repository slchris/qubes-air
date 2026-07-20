<!--
  Qubes Air Console - live terraform output for one job.

  A provision runs 15-25 minutes. Before this, the card showed a disabled
  "Provisioning…" button for the whole time and, on failure, nothing but a red
  dot — the terraform error was returned by the API and thrown away. This tails
  the job's output while it runs and keeps the final output (including the error)
  once it ends.
-->
<script lang="ts">
  import { getJobLog, getJob, streamJobLog } from '../lib/api';

  interface Props {
    jobId: string;
    // Whether the qube is still in a transient state. Used only as the initial
    // guess; the log endpoint's own `running` flag is authoritative once the
    // feed starts, because the job record is what knows it finished.
    active?: boolean;
  }
  let { jobId, active = true }: Props = $props();

  let text = $state('');
  let offset = $state(0);
  let running = $state(active);
  let jobState = $state<string | null>(null);
  let error = $state<string | null>(null);
  let open = $state(active); // auto-expand while an apply is in flight
  let streaming = $state(false); // true while the live stream is attached

  let pre = $state<HTMLPreElement | null>(null);

  const POLL_MS = 3000;

  // Restart cleanly if the card is reused for a different job (Svelte may not
  // remount the component when only the prop changes). The AbortController tears
  // down any in-flight stream from the previous job.
  let current = $state('');
  let controller: AbortController | null = null;
  $effect(() => {
    if (jobId === current) return;
    current = jobId;
    controller?.abort();
    text = '';
    offset = 0;
    running = active;
    error = null;
    open = active;
    void feed();
    return () => controller?.abort();
  });

  // Auto-scroll to the newest line as output arrives, but only when the view is
  // already at (or near) the bottom — so an operator who scrolled up to read
  // something is not yanked back down on every chunk.
  function apply(chunk: { data?: string; offset: number; running: boolean; state?: string }): void {
    if (jobId !== current) return;
    const el = pre;
    const atBottom = el ? el.scrollHeight - el.scrollTop - el.clientHeight < 40 : true;
    if (chunk.data) text += chunk.data;
    offset = chunk.offset;
    running = chunk.running;
    jobState = chunk.state ?? jobState;
    if (el && atBottom) queueMicrotask(() => { el.scrollTop = el.scrollHeight; });
  }

  // Stream first while the job runs, poll as the fallback. The stream gives
  // line-by-line output; when it ends — its own duration cap, or a dropped
  // qrexec forward — control falls through to polling from the last offset, so
  // nothing is missed.
  async function feed(): Promise<void> {
    controller = new AbortController();
    while (running && jobId === current) {
      try {
        streaming = true;
        await streamJobLog(jobId, offset, apply, controller.signal);
        // The stream ended. If the job is still running, loop and reconnect;
        // the offset is where we left off.
      } catch (e) {
        if (controller.signal.aborted) return;
        // Stream unavailable (older console, proxy, a drop). Stop trying to
        // stream and poll instead — same data, resilient to this transport.
        streaming = false;
        void poll();
        return;
      }
      streaming = false;
      if (!running) break;
    }

    // A job that was ALREADY finished when this panel opened never entered the
    // stream loop, so its log has not been fetched. Do one plain read — the log
    // still exists on the server long after the job ended, and without this a
    // succeeded job shows an empty panel even though its full output is there.
    if (jobId === current && !text) {
      await poll();
      return;
    }
    await finish();
  }

  async function poll(): Promise<void> {
    try {
      const chunk = await getJobLog(jobId, offset);
      if (jobId !== current) return; // a newer job took over mid-request
      apply(chunk);
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to read job log';
      running = false;
    }

    if (running && jobId === current) {
      setTimeout(() => void poll(), POLL_MS);
      return;
    }
    await finish();
  }

  // The job finished. If nothing was logged (logs disabled on this console),
  // fall back to the job's own error field so a failure still has a reason.
  async function finish(): Promise<void> {
    if (jobId !== current || text.trim()) return;
    try {
      const job = await getJob(jobId);
      jobState = job.state;
      if (job.error) { text = job.error; open = true; }
    } catch {
      // Leave the panel empty rather than surfacing a secondary error.
    }
  }

  // Strip ANSI colour codes terraform emits; they render as noise in HTML.
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
      <p class="waiting">Waiting for terraform output…</p>
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
