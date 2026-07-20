<!--
  Qubes Air Console - live terraform output for one job.

  A provision runs 15-25 minutes. Before this, the card showed a disabled
  "Provisioning…" button for the whole time and, on failure, nothing but a red
  dot — the terraform error was returned by the API and thrown away. This tails
  the job's output while it runs and keeps the final output (including the error)
  once it ends.
-->
<script lang="ts">
  import { getJobLog, getJob } from '../lib/api';

  interface Props {
    jobId: string;
    // Whether the qube is still in a transient state. Used only as the initial
    // guess; the log endpoint's own `running` flag is authoritative once polling
    // starts, because the job record is what knows it finished.
    active?: boolean;
  }
  let { jobId, active = true }: Props = $props();

  let text = $state('');
  let offset = $state(0);
  let running = $state(active);
  let jobState = $state<string | null>(null);
  let error = $state<string | null>(null);
  let open = $state(active); // auto-expand while an apply is in flight

  const POLL_MS = 3000;

  // Restart cleanly if the card is reused for a different job (Svelte may not
  // remount the component when only the prop changes).
  let current = $state('');
  $effect(() => {
    if (jobId === current) return;
    current = jobId;
    text = '';
    offset = 0;
    running = active;
    error = null;
    open = active;
    void poll();
  });

  async function poll(): Promise<void> {
    try {
      const chunk = await getJobLog(jobId, offset);
      if (jobId !== current) return; // a newer job took over mid-request
      if (chunk.data) text += chunk.data;
      offset = chunk.offset;
      running = chunk.running;
      jobState = chunk.state ?? jobState;
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to read job log';
      running = false;
    }

    if (running) {
      setTimeout(() => void poll(), POLL_MS);
      return;
    }

    // The job finished. If nothing was logged (logs disabled on this console),
    // fall back to the job's own error field so a failure still has a reason.
    if (!text.trim()) {
      try {
        const job = await getJob(jobId);
        jobState = job.state;
        if (job.error) { text = job.error; open = true; }
      } catch {
        // Leave the panel empty rather than surfacing a secondary error.
      }
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
      {#if running}Provisioning — live log{:else if failed}Failed — see log{:else}Job log{/if}
    </span>
    {#if running}<span class="spinner">●</span>{/if}
  </button>

  {#if open}
    {#if error}
      <p class="err">{error}</p>
    {/if}
    {#if clean.trim()}
      <pre>{clean}</pre>
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
    --surface-2: #f9fafb;
    --text: #1a1a1a;
    --muted: #6b7280;
    --border: #e5e7eb;

    margin-top: 0.6rem; border: 1px solid var(--border); border-radius: 4px;
    overflow: hidden;
  }

  @media (prefers-color-scheme: dark) {
    .joblog {
      --surface-2: #1f2937;
      --text: #e5e7eb;
      --muted: #9ca3af;
      --border: #374151;
    }
  }
  .joblog.failed { border-color: #dc2626; }
  .bar {
    width: 100%; display: flex; align-items: center; gap: 0.5rem;
    padding: 0.4rem 0.6rem; background: var(--surface-2); color: var(--text);
    border: none; cursor: pointer; font-size: 0.82rem; text-align: left;
  }
  .joblog.failed .bar { background: #fef2f2; color: #991b1b; }
  .chev { width: 1em; }
  .title { flex: 1; }
  .spinner { color: #2563eb; animation: pulse 1.2s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.25; } }
  pre {
    margin: 0; padding: 0.6rem; max-height: 22rem; overflow: auto;
    background: #0f172a; color: #e2e8f0;
    font-size: 0.72rem; line-height: 1.45; white-space: pre-wrap; word-break: break-word;
  }
  .waiting, .err { margin: 0; padding: 0.6rem; font-size: 0.8rem; color: var(--muted); }
  .err { color: #b91c1c; }
</style>
