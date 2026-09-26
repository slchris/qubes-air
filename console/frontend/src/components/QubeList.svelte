<!--
  Qubes Air Console - Qube List Component

  The qubes view: loads qubes and zones, shows them in QubeTable and opens
  QubeFormDialog for create and edit. Row actions (start, suspend, release,
  purge) live in QubeTable; the form and its capacity lookup live in the dialog.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { qubeStore, zoneStore } from '../lib/stores';
  import type { Zone, Qube } from '../lib/types';
  import QubeTable from './QubeTable.svelte';
  import QubeFormDialog from './QubeFormDialog.svelte';

  // Subscribe to stores
  let qubeState = $state({ qubes: [] as Qube[], loading: false, error: null as string | null, jobs: {} as Record<string, string> });
  let zoneState = $state({ zones: [] as Zone[], loading: false, error: null as string | null });

  $effect(() => {
    const unsubQubes = qubeStore.subscribe(state => { qubeState = state; });
    const unsubZones = zoneStore.subscribe(state => { zoneState = state; });
    return () => { unsubQubes(); unsubZones(); };
  });

  // Derived: connected zones
  let connectedZonesList = $derived(zoneState.zones.filter(z => z.status === 'connected'));

  // Which dialog is open. The dialog is mounted fresh each time, so it always
  // starts from the qube's current values (edit) or the defaults (create).
  let activeForm = $state<{ mode: 'create' | 'edit'; qube: Qube | null } | null>(null);

  onMount(async () => {
    await Promise.all([qubeStore.load(), zoneStore.load()]);
    // A reload lands mid-operation often enough to matter: an apply runs for
    // minutes, so pick the watches back up rather than showing a frozen status.
    qubeStore.resumeWatches();
  });

  function openCreateModal(): void {
    activeForm = { mode: 'create', qube: null };
  }

  function openEditModal(qube: Qube): void {
    activeForm = { mode: 'edit', qube };
  }

  function closeForm(): void {
    activeForm = null;
  }
</script>

<div class="qube-list">
  <div class="header">
    <h2>Remote Qubes</h2>
    <button
      class="btn-primary"
      onclick={openCreateModal}
    >
      + Create Qube
    </button>
  </div>

  {#if qubeState.loading}
    <p class="loading">Loading...</p>
  {:else if qubeState.error}
    <p class="error">{qubeState.error}</p>
  {:else if qubeState.qubes.length === 0}
    <div class="empty">
      <p>No remote qubes</p>
      <p class="hint">Click "+ Create Qube" to create your first qube</p>
    </div>
  {:else}
    <QubeTable
      qubes={qubeState.qubes}
      jobs={qubeState.jobs}
      zones={zoneState.zones}
      onedit={openEditModal}
    />
  {/if}
</div>

{#if activeForm}
  <QubeFormDialog
    mode={activeForm.mode}
    qube={activeForm.qube}
    connectedZones={connectedZonesList}
    zones={zoneState.zones}
    onclose={closeForm}
  />
{/if}

<style>
  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 1rem;
  }

  h2 {
    margin: 0;
  }

  .btn-primary {
    padding: 0.5rem 1rem;
    background: #1976d2;
    color: #fff;
    border: none;
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
  }

  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .loading, .error, .empty {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: #c62828;
  }

  .hint {
    color: var(--label-color, #666);
    font: var(--body);
    margin-top: 0.5rem;
  }

  @media (prefers-color-scheme: dark) {
    .hint {
      --label-color: #aaa;
    }
  }
</style>
