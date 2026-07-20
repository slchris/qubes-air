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
    color: var(--systemPrimary);
  }

  .head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 0.9rem; }
  h2 { margin: 0; font: var(--title-1-emphasized); color: var(--systemPrimary); }
  .ghost {
    border: 1px solid var(--systemQuaternary); background: var(--pageBG); color: var(--systemPrimary);
    border-radius: var(--global-border-radius-xsmall); padding: 0.35rem 0.7rem; font: var(--callout); cursor: pointer;
  }
  .banner {
    margin: 0 0 1rem; padding: 0.6rem 0.8rem; border-radius: var(--global-border-radius-xsmall);
    border: 1px solid #d97706; background: #fef3c7; color: #7c2d12; font: var(--body);
  }
  .empty { margin: 0; font: var(--body); color: var(--systemSecondary); line-height: 1.55; }

  .rows { list-style: none; margin: 0; padding: 0; border: 1px solid var(--systemQuaternary); border-radius: var(--global-border-radius-small); overflow: hidden; }
  .rows li { border-top: 1px solid var(--systemQuaternary); background: var(--pageBG); }
  .rows li:first-child { border-top: none; }
  .rows li.failed { background: color-mix(in srgb, var(--systemRed) 6%, var(--pageBG)); }

  .row {
    width: 100%; display: flex; align-items: center; gap: 0.75rem;
    padding: 0.55rem 0.8rem; background: none; border: none; cursor: pointer;
    color: var(--systemPrimary); font: var(--body); text-align: left;
  }
  .name { font-weight: 500; min-width: 9rem; }
  .action { min-width: 5rem; }
  .state { min-width: 5rem; }
  .meta { color: var(--systemSecondary); font: var(--callout); white-space: nowrap; }
  .time { margin-left: auto; }
  .chev { color: var(--systemSecondary); width: 1em; }

  .detail { padding: 0 0.8rem 0.7rem; }

  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--systemSecondary); }
  .dot.succeeded { background: var(--systemGreen); }
  .dot.failed { background: var(--systemRed); }
  .dot.running, .dot.queued { background: var(--keyColor); }
</style>
