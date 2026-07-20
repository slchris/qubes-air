<!--
  Qubes Air Console - job history.

  The audit trail: every infrastructure change this console made, including the
  ones that failed and terraform's own error text. The API has served this from
  the start and nothing called it, so a failed provision could only be
  investigated through the API by hand.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { listJobs } from '../lib/api';
  import type { Job } from '../lib/types';
  import JobLog from './JobLog.svelte';

  let jobs = $state<Job[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let expanded = $state<string | null>(null);

  async function load(): Promise<void> {
    loading = true;
    error = null;
    try {
      const r = await listJobs(undefined, 100);
      jobs = r.jobs ?? [];
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to load jobs';
      jobs = [];
    } finally {
      loading = false;
    }
  }

  onMount(load);

  function when(iso: string | undefined): string {
    return iso ? new Date(iso).toLocaleString() : '—';
  }

  // Elapsed time, which is the number that tells you whether an apply is slow
  // or stuck — a provision runs 15-25 minutes and neither end of that range is
  // obvious from a pair of timestamps.
  function took(j: Job): string {
    if (!j.started_at) return '';
    const end = j.finished_at ? new Date(j.finished_at).getTime() : Date.now();
    const s = Math.max(0, (end - new Date(j.started_at).getTime()) / 1000);
    if (s < 60) return `${Math.round(s)}s`;
    const m = Math.floor(s / 60);
    return `${m}m ${Math.round(s % 60)}s`;
  }
</script>

<div class="jobs">
  <div class="head">
    <h2>Jobs</h2>
    <button class="ghost" onclick={load} disabled={loading}>
      {loading ? 'Loading…' : 'Refresh'}
    </button>
  </div>

  {#if error}
    <p class="banner">{error}</p>
  {:else if jobs.length === 0 && !loading}
    <p class="empty">
      No jobs yet. Creating, resuming or releasing a qube records one here —
      including its terraform output.
    </p>
  {:else}
    <ul class="rows">
      {#each jobs as j (j.id)}
        <li class:failed={j.state === 'failed'}>
          <button class="row" onclick={() => (expanded = expanded === j.id ? null : j.id)}>
            <span class="dot {j.state}"></span>
            <span class="name">{j.qube_name}</span>
            <span class="meta action">{j.action}</span>
            <span class="meta state">{j.state}</span>
            <span class="meta">{took(j)}</span>
            <span class="meta time">{when(j.finished_at ?? j.started_at ?? j.enqueued_at)}</span>
            <span class="chev">{expanded === j.id ? '▾' : '▸'}</span>
          </button>
          {#if expanded === j.id}
            <div class="detail">
              <!-- The log panel falls back to the job's error field when no
                   output was recorded, so a failure always has a reason here. -->
              <JobLog jobId={j.id} active={j.state === 'running' || j.state === 'queued'} />
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .jobs {
    --surface: #ffffff;
    --text: #1a1a1a;
    --muted: #6b7280;
    --border: #e5e7eb;
    color: var(--text);
    max-width: 1100px;
  }
  @media (prefers-color-scheme: dark) {
    .jobs {
      --surface: #1f2937;
      --text: #e5e7eb;
      --muted: #9ca3af;
      --border: #374151;
    }
  }

  .head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 0.9rem; }
  h2 { margin: 0; font-size: 1.3rem; color: var(--text); }
  .ghost {
    border: 1px solid var(--border); background: var(--surface); color: var(--text);
    border-radius: 4px; padding: 0.35rem 0.7rem; font-size: 0.82rem; cursor: pointer;
  }
  .banner {
    margin: 0 0 1rem; padding: 0.6rem 0.8rem; border-radius: 4px;
    border: 1px solid #d97706; background: #fef3c7; color: #7c2d12; font-size: 0.88rem;
  }
  .empty { margin: 0; font-size: 0.87rem; color: var(--muted); line-height: 1.55; }

  .rows { list-style: none; margin: 0; padding: 0; border: 1px solid var(--border); border-radius: 6px; overflow: hidden; }
  .rows li { border-top: 1px solid var(--border); background: var(--surface); }
  .rows li:first-child { border-top: none; }
  .rows li.failed { background: color-mix(in srgb, #dc2626 6%, var(--surface)); }

  .row {
    width: 100%; display: flex; align-items: center; gap: 0.75rem;
    padding: 0.55rem 0.8rem; background: none; border: none; cursor: pointer;
    color: var(--text); font-size: 0.85rem; text-align: left;
  }
  .name { font-weight: 500; min-width: 9rem; }
  .action { min-width: 5rem; }
  .state { min-width: 5rem; }
  .meta { color: var(--muted); font-size: 0.78rem; white-space: nowrap; }
  .time { margin-left: auto; }
  .chev { color: var(--muted); width: 1em; }

  .detail { padding: 0 0.8rem 0.7rem; }

  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: #9ca3af; }
  .dot.succeeded { background: #16a34a; }
  .dot.failed { background: #dc2626; }
  .dot.running, .dot.queued { background: #2563eb; }
</style>
