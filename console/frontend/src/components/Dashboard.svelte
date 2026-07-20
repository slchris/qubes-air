<!--
  Qubes Air Console - Dashboard.

  The landing view. Before this the console opened straight onto the qube list,
  which answers "what qubes exist" but not "is anything wrong" — and the two
  facts that matter most on arrival are exactly the ones that were hardest to
  see: a qube that is running while its agent is unreachable, and a job that
  failed. Both were reachable only by opening a card or the API.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { qubeStore, zoneStore } from '../lib/stores';
  import type { Qube, Zone, Job } from '../lib/types';
  import { listJobs } from '../lib/api';

  interface Props {
    onViewChange?: (view: string) => void;
  }
  let { onViewChange }: Props = $props();

  let qs = $state({ qubes: [] as Qube[], loading: false, error: null as string | null, jobs: {} as Record<string, string> });
  let zonesState = $state({ zones: [] as Zone[], loading: false, error: null as string | null });
  $effect(() => {
    const a = qubeStore.subscribe(s => { qs = s; });
    const b = zoneStore.subscribe(s => { zonesState = s; });
    return () => { a(); b(); };
  });

  let recentJobs = $state<Job[]>([]);
  let jobsError = $state<string | null>(null);

  onMount(async () => {
    await Promise.all([qubeStore.load(), zoneStore.load()]);
    try {
      const r = await listJobs(undefined, 8);
      recentJobs = r.jobs ?? [];
    } catch (e) {
      // A console with orchestration disabled has no job history; that is not
      // an error worth a red banner on the landing page.
      jobsError = e instanceof Error ? e.message : 'Could not read job history';
    }
  });

  const running = $derived(qs.qubes.filter(q => q.status === 'running'));
  const parked = $derived(qs.qubes.filter(q => q.status === 'suspended' || q.status === 'released'));
  const busy = $derived(qs.qubes.filter(q =>
    ['creating', 'resuming', 'suspending', 'deleting'].includes(q.status)));
  const failed = $derived(qs.qubes.filter(q => q.status === 'error'));

  // The case this dashboard exists for: the qube is up, so the status dot is
  // green, but the console cannot reach its agent. Nothing else surfaces it.
  const unreachable = $derived(running.filter(q => q.agent_health === 'unreachable'));

  const connectedZones = $derived(zonesState.zones.filter(z => z.status === 'connected'));
  const failedJobs = $derived(recentJobs.filter(j => j.state === 'failed'));

  function go(view: string): void {
    if (onViewChange) onViewChange(view);
    else window.location.hash = view;
  }

  function ago(iso: string | undefined): string {
    if (!iso) return '';
    const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (s < 60) return `${Math.round(s)}s ago`;
    if (s < 3600) return `${Math.round(s / 60)}m ago`;
    if (s < 86400) return `${Math.round(s / 3600)}h ago`;
    return `${Math.round(s / 86400)}d ago`;
  }
</script>

<div class="dash">
  <div class="head">
    <h2>Overview</h2>
    <button class="ghost" onclick={() => { qubeStore.load(); zoneStore.load(); }}>Refresh</button>
  </div>

  <!-- Problems first. An overview that leads with totals buries the one line
       the operator needed to see. These render only when there is something
       wrong, so a healthy fleet shows no alarm furniture at all. -->
  {#if unreachable.length > 0}
    <button class="alert warn" onclick={() => go('qubes')}>
      <strong>{unreachable.length}</strong>
      {unreachable.length === 1 ? 'qube is' : 'qubes are'} running but their agent is unreachable
      <span class="names">{unreachable.map(q => q.name).join(', ')}</span>
    </button>
  {/if}

  {#if failed.length > 0}
    <button class="alert bad" onclick={() => go('qubes')}>
      <strong>{failed.length}</strong>
      {failed.length === 1 ? 'qube' : 'qubes'} in error
      <span class="names">{failed.map(q => q.name).join(', ')}</span>
    </button>
  {/if}

  {#if failedJobs.length > 0}
    <button class="alert bad" onclick={() => go('jobs')}>
      <strong>{failedJobs.length}</strong> recent
      {failedJobs.length === 1 ? 'job' : 'jobs'} failed
      <span class="names">{failedJobs.map(j => `${j.qube_name} (${j.action})`).join(', ')}</span>
    </button>
  {/if}

  <div class="tiles">
    <button class="tile" onclick={() => go('qubes')}>
      <span class="n">{qs.qubes.length}</span>
      <span class="l">Qubes</span>
      <span class="sub">
        {running.length} running · {parked.length} parked{#if busy.length} · {busy.length} working{/if}
      </span>
    </button>

    <button class="tile" onclick={() => go('zones')}>
      <span class="n">{zonesState.zones.length}</span>
      <span class="l">Zones</span>
      <span class="sub">{connectedZones.length} connected</span>
    </button>

    <button class="tile" onclick={() => go('jobs')}>
      <span class="n">{recentJobs.length}</span>
      <span class="l">Recent jobs</span>
      <span class="sub">
        {#if jobsError}unavailable{:else if failedJobs.length}{failedJobs.length} failed{:else}all clear{/if}
      </span>
    </button>
  </div>

  <section>
    <div class="sec-head">
      <h3>Qubes</h3>
      <button class="link" onclick={() => go('qubes')}>View all</button>
    </div>
    {#if qs.qubes.length === 0}
      <p class="empty">
        No qubes yet. Create one from the Qubes view — it needs a connected zone first.
      </p>
    {:else}
      <ul class="rows">
        {#each qs.qubes.slice(0, 6) as q (q.id)}
          <li>
            <span class="dot {q.status}"></span>
            <span class="name">{q.name}</span>
            <span class="meta">{q.status}</span>
            <span class="meta agent {q.agent_health ?? 'unknown'}">
              agent {q.agent_health ?? 'unknown'}
            </span>
            <span class="meta mono">{q.ip_address ?? '—'}</span>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section>
    <div class="sec-head">
      <h3>Recent activity</h3>
      <button class="link" onclick={() => go('jobs')}>View all</button>
    </div>
    {#if jobsError}
      <p class="empty">Job history unavailable ({jobsError}).</p>
    {:else if recentJobs.length === 0}
      <p class="empty">Nothing has run yet.</p>
    {:else}
      <ul class="rows">
        {#each recentJobs.slice(0, 6) as j (j.id)}
          <li>
            <span class="dot job-{j.state}"></span>
            <span class="name">{j.qube_name}</span>
            <span class="meta">{j.action}</span>
            <span class="meta">{j.state}</span>
            <span class="meta">{ago(j.finished_at ?? j.started_at ?? j.enqueued_at)}</span>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<style>
  .dash {
    color: var(--systemPrimary);
    max-width: 1100px;
  }

  .head, .sec-head {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.75rem;
  }
  h2 { margin: 0; font: var(--title-1-emphasized); color: var(--systemPrimary); }
  h3 { margin: 0; font: var(--title-2-emphasized); color: var(--systemPrimary); }

  .ghost, .link {
    border: 1px solid var(--systemQuaternary); background: var(--pageBG); color: var(--systemPrimary);
    border-radius: var(--global-border-radius-xsmall); padding: 0.35rem 0.7rem; font: var(--callout); cursor: pointer;
  }
  .link { border-color: transparent; color: var(--keyColor); padding: 0.2rem 0.3rem; }

  .alert {
    display: block; width: 100%; text-align: left; cursor: pointer;
    margin-bottom: 0.6rem; padding: 0.65rem 0.85rem; border-radius: var(--global-border-radius-xsmall);
    font: var(--body); line-height: 1.45;
  }
  .alert .names { display: block; font: var(--callout); opacity: 0.85; margin-top: 0.15rem; }
  /* Fixed foregrounds: these keep their background in both schemes. */
  .alert.warn { border: 1px solid #d97706; background: #fef3c7; color: #7c2d12; }
  .alert.bad { border: 1px solid var(--systemRed); background: #fef2f2; color: #991b1b; }

  .tiles {
    display: grid; gap: 0.75rem; margin: 1rem 0 1.5rem;
    grid-template-columns: repeat(auto-fit, minmax(min(12rem, 100%), 1fr));
  }
  .tile {
    display: flex; flex-direction: column; gap: 0.15rem; text-align: left;
    padding: 0.9rem 1rem; border: 1px solid var(--systemQuaternary); border-radius: var(--global-border-radius-small);
    background: var(--pageBG); color: var(--systemPrimary); cursor: pointer;
  }
  .tile .n { font: var(--large-title-emphasized); font-weight: 600; line-height: 1.1; }
  .tile .l { font: var(--body); }
  .tile .sub { font: var(--subhead); color: var(--systemSecondary); }

  section { margin-bottom: 1.5rem; }
  .empty { margin: 0; font: var(--body); color: var(--systemSecondary); line-height: 1.5; }

  .rows { list-style: none; margin: 0; padding: 0; border: 1px solid var(--systemQuaternary); border-radius: var(--global-border-radius-small); overflow: hidden; }
  .rows li {
    display: flex; align-items: center; gap: 0.75rem;
    padding: 0.5rem 0.8rem; background: var(--pageBG);
    border-top: 1px solid var(--systemQuaternary); font: var(--body);
  }
  .rows li:first-child { border-top: none; }
  .name { font-weight: 500; flex: 1; min-width: 8rem; }
  .meta { color: var(--systemSecondary); font: var(--callout); white-space: nowrap; }
  .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .agent.healthy { color: var(--systemGreen); }
  .agent.unreachable { color: var(--systemRed); }

  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--systemSecondary); }
  .dot.running, .dot.job-succeeded { background: var(--systemGreen); }
  .dot.error, .dot.job-failed { background: var(--systemRed); }
  .dot.creating, .dot.resuming, .dot.suspending, .dot.deleting, .dot.job-running { background: var(--keyColor); }
  .dot.suspended, .dot.released { background: #7e57c2; }
</style>
