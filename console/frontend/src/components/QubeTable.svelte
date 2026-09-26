<!--
  Qubes Air Console - Qube table

  One row per qube: status, agent health, placement, spec, address and the
  lifecycle actions. The create/edit form lives in QubeFormDialog; the
  container (QubeList) owns loading and which dialog is open.
-->
<script lang="ts">
  import { qubeStore } from '../lib/stores';
  import { ApiException } from '../lib/api';
  import { isTransientStatus, hasCompute } from '../lib/types';
  import type { AgentHealth, Qube, Zone } from '../lib/types';
  import JobLog from './JobLog.svelte';

  let {
    qubes,
    jobs,
    zones,
    onedit,
  }: {
    qubes: Qube[];
    jobs: Record<string, string>;
    zones: Zone[];
    onedit: (qube: Qube) => void;
  } = $props();

  // The agent-health label. "running + agent unhealthy" is the case worth
  // spelling out — a green status dot for a qube whose agent cannot be reached
  // is the failure this field exists to surface. "starting" is shown as itself:
  // folding it into "unknown" hid that the console is probing a qube that has
  // only just booted, where silence is expected rather than a fault.
  function agentLabel(h: AgentHealth | undefined): string {
    switch (h) {
      case 'healthy': return 'healthy';
      case 'unreachable': return 'unreachable';
      case 'starting': return 'starting';
      default: return 'unknown';
    }
  }

  // Marks the failures that need a human. The wording stays on what the console
  // measured — nothing has answered for longer than the unit's own restart
  // budget — because the unit itself is inside the guest and this process cannot
  // read it. It also does not claim the agent is gone for good: the unit is
  // enabled and starts again at boot, so what is actually true is that it has
  // stopped restarting within this boot.
  function agentRecoveryHint(q: Qube): string {
    const since = q.agent_failing_since
      ? ` since ${new Date(q.agent_failing_since).toLocaleString()}`
      : '';
    return `No probe has answered${since} — longer than the qubes-air-agent unit's whole ` +
      `restart budget (5 starts within 300s), so in this boot it has stopped restarting ` +
      `itself and needs systemctl reset-failed. ` +
      `The console cannot read the unit inside the qube: check ` +
      `systemctl status qubes-air-agent there ` +
      `(see docs/runbook-remotevm.md).`;
  }

  function getStatusColor(status: string): string {
    switch (status) {
      case 'running': return '#4caf50';
      case 'stopped': return '#9e9e9e';
      // Suspended and released both mean "compute gone, data disk kept". They
      // are shown distinctly from stopped because they are the cheap state the
      // whole compute/storage separation exists to provide.
      case 'suspended': return '#7e57c2';
      case 'released': return '#616161';
      case 'purged': return '#37474f';
      case 'error': return '#f44336';
      case 'creating':
      case 'resuming':
      case 'suspending':
      case 'deleting': return '#2196f3';
      default: return '#ff9800';
    }
  }

  /**
   * Human-readable label for a status. Transient ones read as verbs so the UI
   * says what is happening rather than showing an opaque noun for the several
   * minutes a terraform apply takes.
   */
  function getStatusLabel(status: string): string {
    switch (status) {
      case 'creating': return 'Provisioning…';
      case 'resuming': return 'Resuming…';
      case 'suspending': return 'Suspending…';
      case 'deleting': return 'Deleting…';
      case 'suspended': return 'Suspended (data kept)';
      case 'released': return 'Released (data kept)';
      case 'purged': return 'Purged';
      default: return status;
    }
  }

  /** A qube can be resumed from any state where its compute is not running. */
  function canStart(status: string): boolean {
    return status === 'stopped' || status === 'suspended'
      || status === 'released' || status === 'error';
  }

  function getZoneName(zoneId: string | undefined): string {
    if (!zoneId) return 'No Zone';
    return zones.find(z => z.id === zoneId)?.name ?? 'Unknown';
  }

  async function handleDelete(qube: Qube): Promise<void> {
    if (!confirm(`Delete qube "${qube.name}"?`)) return;

    try {
      await qubeStore.remove(qube.id);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to delete qube');
    }
  }

  async function handleStart(qube: Qube): Promise<void> {
    try {
      await qubeStore.start(qube.id);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to start qube');
    }
  }

  async function handleStop(qube: Qube): Promise<void> {
    try {
      await qubeStore.stop(qube.id);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to stop qube');
    }
  }

  // Purge is irreversible, so it takes two confirmations: a dialog, then typing
  // the qube's exact name — the same value the backend checks.
  async function handlePurge(qube: Qube): Promise<void> {
    if (!confirm(`Permanently destroy qube "${qube.name}" and its data disk? This cannot be undone.`)) return;
    const typed = prompt(`Type the qube name "${qube.name}" to confirm:`);
    if (typed !== qube.name) {
      if (typed !== null) alert('Name did not match; purge cancelled.');
      return;
    }
    try {
      await qubeStore.purge(qube.id, qube.name);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to purge qube');
    }
  }
</script>

<!-- A list, not a card grid. A fleet is scanned down a column — status,
     agent health, address — and cards force that comparison to happen
     across two dimensions. The row stays one line; its job log expands
     underneath on demand. -->
<div class="qube-table" role="table">
  <div class="qhead" role="row">
    <span class="c-name">Name</span>
    <span class="c-status">Status</span>
    <span class="c-agent">Agent</span>
    <span class="c-zone">Zone / node</span>
    <span class="c-spec">Spec</span>
    <span class="c-ip">Address</span>
    <span class="c-act"></span>
  </div>

  {#each qubes as qube (qube.id)}
    <div class="qrow-wrap" class:has-log={!!jobs[qube.id]}>
      <div class="qrow" role="row">
        <span class="c-name">
          <span class="status-dot" style="background: {getStatusColor(qube.status)}"
                title={getStatusLabel(qube.status)}></span>
          <span class="nm">{qube.name}</span>
          <code class="ty">{qube.type}</code>
        </span>

        <span class="c-status">{getStatusLabel(qube.status)}</span>

        <span class="c-agent">
          <span class="agent {qube.agent_health ?? 'unknown'}"
                title={qube.agent_last_error || ''}>{agentLabel(qube.agent_health)}</span>
          {#if qube.agent_recovery === 'manual' && hasCompute(qube.status)}
            <!-- A second line, not a second state: the health pill still
                 says "unreachable", and this says what to do about it.
                 Only where a compute instance can exist: a parked qube keeps
                 the reading it had before it was parked, and nothing is
                 probing it (see computeRunning in qube_predicates.go). -->
            <span class="agent-manual" title={agentRecoveryHint(qube)}>
              manual recovery
            </span>
          {/if}
          {#if qube.agent_health === 'unreachable' && qube.agent_last_error}
            <span class="agent-err" title={qube.agent_last_error}>{qube.agent_last_error}</span>
          {/if}
        </span>

        <span class="c-zone">
          {getZoneName(qube.zone_id)}{#if qube.spec.node}<span class="dim"> · {qube.spec.node}</span>{/if}
        </span>

        <span class="c-spec">
          {qube.spec.vcpu}c / {qube.spec.memory}M / {qube.spec.disk}G{#if qube.spec.data_disk_gb}<span class="dim"> +{qube.spec.data_disk_gb}G</span>{/if}
        </span>

        <span class="c-ip mono">{qube.ip_address || '—'}</span>

        <span class="c-act">
          {#if isTransientStatus(qube.status)}
            <!-- An operation is in flight. The backend refuses a second one,
                 so this is disabled rather than offering a click that comes
                 back 409. -->
            <button class="btn" disabled>{getStatusLabel(qube.status)}</button>
          {:else if qube.purge_requested}
            <button class="btn" disabled>{qube.status === 'purged' ? 'Purged' : 'Purge pending'}</button>
          {:else if canStart(qube.status)}
            <button class="btn" onclick={() => handleStart(qube)}>
              {qube.status === 'suspended' || qube.status === 'released' ? 'Resume' : 'Start'}
            </button>
          {:else if qube.status === 'running'}
            <button class="btn" onclick={() => handleStop(qube)}
                    title="Destroy the compute instance and keep the data disk">Suspend</button>
          {:else}
            <button class="btn" disabled>{getStatusLabel(qube.status)}</button>
          {/if}
          <button class="btn btn-secondary" onclick={() => onedit(qube)}
                  disabled={isTransientStatus(qube.status) || qube.purge_requested}>Edit</button>
          <button class="btn btn-danger" onclick={() => handleDelete(qube)}
                  disabled={isTransientStatus(qube.status) || qube.purge_requested || qube.status === 'released'}
                  title="Release the compute instance. The data disk is kept and can be purged separately."
          >Release</button>
          {#if ['released', 'suspended', 'stopped', 'error'].includes(qube.status)}
            <button class="btn btn-danger" onclick={() => handlePurge(qube)}
                    disabled={isTransientStatus(qube.status)}
                    title="Permanently destroy the data disk and revoke the agent identity. Irreversible."
            >{qube.purge_requested ? 'Retry purge' : 'Purge'}</button>
          {/if}
        </span>
      </div>

      {#if jobs[qube.id]}
        <div class="qrow-log">
          <JobLog jobId={jobs[qube.id]} active={isTransientStatus(qube.status)} />
        </div>
      {/if}
    </div>
  {/each}
</div>

<style>
  /* --- list layout ---------------------------------------------------------
     One grid template shared by the header and every row, so columns line up
     without a <table> (rows need to expand into a log panel, which a table row
     cannot contain cleanly). */
  .qube-table {
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-small);
    /* Safety net: if a column set ever exceeds the container again, the row
       scrolls instead of hiding its controls. */
    overflow-x: auto;
    color: var(--systemPrimary);
  }

  .qhead, .qrow {
    display: grid;
    /* Minimums kept small on purpose. The first version summed to more than the
       content area (a 1000px window minus the 200px sidebar), so the grid
       overflowed a container with overflow:hidden and the action buttons were
       not merely cramped — they were unreachable. */
    grid-template-columns:
      minmax(7rem, 1.3fr)   /* name */
      minmax(4.5rem, 0.8fr) /* status */
      minmax(4.5rem, 0.8fr) /* agent */
      minmax(5rem, 0.9fr)   /* zone/node */
      minmax(6rem, 1fr)     /* spec */
      minmax(5rem, 0.8fr)   /* address */
      auto;                 /* actions */
    gap: 0.75rem;
    align-items: center;
    padding: 0.5rem 0.8rem;
  }
  .qhead {
    background: color-mix(in srgb, var(--systemPrimary) 5%, var(--pageBG));
    color: var(--systemSecondary);
    font: var(--subhead);
    text-transform: uppercase;
    letter-spacing: 0;
  }
  .qrow-wrap { border-top: 1px solid var(--systemQuaternary); background: var(--pageBG); }
  .qrow-wrap:first-of-type { border-top: none; }
  .qrow { font: var(--body); }
  .qrow-log { padding: 0 0.8rem 0.6rem; }

  .c-name { display: flex; align-items: center; gap: 0.5rem; min-width: 0; }
  .c-name .nm { font-weight: 500; overflow: hidden; text-overflow: ellipsis; }
  .c-name .ty {
    font: var(--footnote); color: var(--systemSecondary); border: 1px solid var(--systemQuaternary);
    border-radius: 3px; padding: 0 0.25rem;
  }
  .c-status, .c-zone, .c-spec, .c-ip { color: var(--systemPrimary); min-width: 0; }
  .c-ip { font: var(--callout); }
  .dim { color: var(--systemSecondary); }
  .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .c-act { display: flex; gap: 0.35rem; justify-content: flex-end; }
  .c-agent { display: flex; flex-direction: column; min-width: 0; }
  .agent-manual {
    font: var(--footnote); color: var(--systemRed); font-weight: 500;
    border: 1px solid currentColor; border-radius: 3px; padding: 0 0.25rem;
    align-self: flex-start; white-space: nowrap;
  }
  .agent-err {
    font: var(--footnote); color: var(--systemRed);
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }

  /* Progressive disclosure rather than one hard switch to stacked blocks.
     Stacking at the first sign of pressure turned a 1000px window — an ordinary
     desktop size — into three screen-filling blocks, which is the opposite of a
     list. Spec is the first column to go because it is the one nobody scans
     down; the columns that answer "is this thing healthy and where is it" stay
     until there is genuinely no room. */
  @media (max-width: 1080px) {
    .qhead, .qrow { grid-template-columns:
      minmax(7rem, 1.3fr) minmax(4.5rem, 0.8fr) minmax(4.5rem, 0.8fr)
      minmax(5rem, 0.9fr) minmax(5rem, 0.8fr) auto; }
    .c-spec { display: none; }
  }

  /* Below this the seven columns stop being readable; each row becomes a
     stacked block with its own labels rather than a squeezed grid. */
  @media (max-width: 820px) {
    .qhead { display: none; }
    .qrow {
      grid-template-columns: 1fr;
      gap: 0.3rem;
      padding: 0.7rem 0.8rem;
    }
    .c-spec { display: block; }
    .c-act { justify-content: flex-start; margin-top: 0.5rem; }
    /* Labels, because without the header row a bare value says nothing. */
    .c-status::before { content: 'Status: '; color: var(--systemSecondary); }
    .c-agent::before { content: 'Agent: '; color: var(--systemSecondary); }
    .c-zone::before { content: 'Zone: '; color: var(--systemSecondary); }
    .c-spec::before { content: 'Spec: '; color: var(--systemSecondary); }
    .c-ip::before { content: 'Address: '; color: var(--systemSecondary); }
    /* The stacked agent cell is a column; the label needs to sit inline with
       the value rather than above it. */
    .c-agent { flex-direction: row; gap: 0.3rem; align-items: baseline; flex-wrap: wrap; }
  }

  .agent { font-weight: 500; }
  .agent.healthy { color: var(--systemGreen); }
  .agent.unreachable { color: var(--systemRed); }
  .agent.unknown, .agent.starting { color: var(--systemSecondary); }

  .status-dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
  }

  code {
    padding: 0.125rem 0.375rem;
    background: var(--code-bg, #e8e8e8);
    border-radius: 3px;
    font: var(--body);
  }

  .btn {
    flex: 1;
    padding: 0.5rem;
    background: #1976d2;
    color: #fff;
    border: none;
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
    font: var(--body);
  }

  .btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-secondary {
    background: #e0e0e0;
    color: #333;
  }

  .btn-danger {
    background: #ffcdd2;
    color: #c62828;
  }

  /* Compact controls: three full-width buttons per row read as three separate
     calls to action, when they are one row's controls. */
  .c-act .btn {
    padding: 0.3rem 0.6rem;
    font: var(--callout);
    white-space: nowrap;
  }
  @media (max-width: 820px) {
    .c-act .btn { flex: 0 0 auto; }
  }

  @media (prefers-color-scheme: dark) {
    code {
      --code-bg: #404040;
    }
  }
</style>
