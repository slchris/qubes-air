<!--
  Qubes Air Console - Zone List Component

  Displays zones with CRUD operations and status management.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { zoneStore } from '../lib/stores';
  import type { Zone, ZoneType, ZoneCreateRequest } from '../lib/types';
  import { ApiException } from '../lib/api';

  // Subscribe to store
  let storeState = $state({ zones: [] as Zone[], loading: false, error: null as string | null });
  
  $effect(() => {
    const unsubscribe = zoneStore.subscribe(state => {
      storeState = state;
    });
    return unsubscribe;
  });

  // Modal state
  let showCreateModal = $state(false);
  let showEditModal = $state(false);
  let editingZone = $state<Zone | null>(null);
  let actionError = $state<string | null>(null);

  // Form data
  let formName = $state('');
  let formType = $state<ZoneType>('proxmox');
  let formEndpoint = $state('');
  let formRegion = $state('');

  const zoneTypes: ZoneType[] = ['proxmox', 'gcp', 'aws', 'azure'];

  onMount(() => {
    zoneStore.load();
  });

  function getStatusColor(status: string): string {
    return status === 'connected' ? '#4caf50' : '#f44336';
  }

  function resetForm(): void {
    formName = '';
    formType = 'proxmox';
    formEndpoint = '';
    formRegion = '';
    actionError = null;
  }

  function openCreateModal(): void {
    resetForm();
    showCreateModal = true;
  }

  function openEditModal(zone: Zone): void {
    editingZone = zone;
    formName = zone.name;
    formType = zone.type;
    formEndpoint = zone.config.endpoint;
    formRegion = zone.config.region ?? '';
    actionError = null;
    showEditModal = true;
  }

  function closeModals(): void {
    showCreateModal = false;
    showEditModal = false;
    editingZone = null;
    resetForm();
  }

  async function handleCreate(): Promise<void> {
    actionError = null;
    const data: ZoneCreateRequest = {
      name: formName.trim(),
      type: formType,
      config: {
        endpoint: formEndpoint.trim(),
        region: formRegion.trim() || undefined,
      },
    };

    try {
      await zoneStore.create(data);
      closeModals();
    } catch (e) {
      actionError = e instanceof ApiException ? e.message : 'Failed to create zone';
    }
  }

  async function handleUpdate(): Promise<void> {
    if (!editingZone) return;
    actionError = null;

    try {
      await zoneStore.updateZone(editingZone.id, {
        name: formName.trim(),
        config: {
          endpoint: formEndpoint.trim(),
          region: formRegion.trim() || undefined,
        },
      });
      closeModals();
    } catch (e) {
      actionError = e instanceof ApiException ? e.message : 'Failed to update zone';
    }
  }

  async function handleDelete(zone: Zone): Promise<void> {
    if (!confirm(`Delete zone "${zone.name}"?`)) return;

    try {
      await zoneStore.remove(zone.id);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to delete zone');
    }
  }

  async function handleConnect(zone: Zone): Promise<void> {
    try {
      await zoneStore.connect(zone.id);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to connect');
    }
  }

  async function handleDisconnect(zone: Zone): Promise<void> {
    try {
      await zoneStore.disconnect(zone.id);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to disconnect');
    }
  }

  function handleSubmitCreate(e: Event): void {
    e.preventDefault();
    handleCreate();
  }

  function handleSubmitUpdate(e: Event): void {
    e.preventDefault();
    handleUpdate();
  }

  function stopPropagation(e: Event): void {
    e.stopPropagation();
  }
</script>

<div class="zone-list">
  <div class="header">
    <h2>Zones</h2>
    <button class="btn-primary" onclick={openCreateModal}>+ Add Zone</button>
  </div>

  {#if storeState.loading}
    <p class="loading">Loading...</p>
  {:else if storeState.error}
    <p class="error">{storeState.error}</p>
  {:else if storeState.zones.length === 0}
    <p class="empty">No zones configured</p>
  {:else}
    <table class="table">
      <thead>
        <tr>
          <th>Name</th>
          <th>Type</th>
          <th>Endpoint</th>
          <th>Status</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        {#each storeState.zones as zone (zone.id)}
          <tr>
            <td>{zone.name}</td>
            <td><code>{zone.type}</code></td>
            <td class="endpoint">{zone.config.endpoint}</td>
            <td>
              <span
                class="status-badge"
                style="--status-color: {getStatusColor(zone.status)}"
              >
                {zone.status}
              </span>
            </td>
            <td class="actions">
              {#if zone.status === 'disconnected'}
                <button class="btn-small" onclick={() => handleConnect(zone)}>Connect</button>
              {:else}
                <button class="btn-small" onclick={() => handleDisconnect(zone)}>Disconnect</button>
              {/if}
              <button class="btn-small" onclick={() => openEditModal(zone)}>Edit</button>
              <button class="btn-small btn-danger" onclick={() => handleDelete(zone)}>Delete</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<!-- Create Modal -->
{#if showCreateModal}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal-overlay" onclick={closeModals}>
    <div class="modal" onclick={stopPropagation}>
      <h3>Create Zone</h3>

      {#if actionError}
        <p class="form-error">{actionError}</p>
      {/if}

      <form onsubmit={handleSubmitCreate}>
        <div class="form-group">
          <label for="name">Name</label>
          <input id="name" type="text" bind:value={formName} required />
        </div>

        <div class="form-group">
          <label for="type">Type</label>
          <select id="type" bind:value={formType}>
            {#each zoneTypes as t}
              <option value={t}>{t}</option>
            {/each}
          </select>
        </div>

        <div class="form-group">
          <label for="endpoint">Endpoint</label>
          <input id="endpoint" type="text" bind:value={formEndpoint} required placeholder="https://..." />
        </div>

        <div class="form-group">
          <label for="region">Region (optional)</label>
          <input id="region" type="text" bind:value={formRegion} placeholder="us-east-1" />
        </div>

        <div class="form-actions">
          <button type="button" class="btn-secondary" onclick={closeModals}>Cancel</button>
          <button type="submit" class="btn-primary">Create</button>
        </div>
      </form>
    </div>
  </div>
{/if}

<!-- Edit Modal -->
{#if showEditModal && editingZone}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal-overlay" onclick={closeModals}>
    <div class="modal" onclick={stopPropagation}>
      <h3>Edit Zone</h3>

      {#if actionError}
        <p class="form-error">{actionError}</p>
      {/if}

      <form onsubmit={handleSubmitUpdate}>
        <div class="form-group">
          <label for="edit-name">Name</label>
          <input id="edit-name" type="text" bind:value={formName} required />
        </div>

        <div class="form-group">
          <label for="edit-type">Type</label>
          <input id="edit-type" type="text" value={editingZone.type} disabled />
        </div>

        <div class="form-group">
          <label for="edit-endpoint">Endpoint</label>
          <input id="edit-endpoint" type="text" bind:value={formEndpoint} required />
        </div>

        <div class="form-group">
          <label for="edit-region">Region (optional)</label>
          <input id="edit-region" type="text" bind:value={formRegion} />
        </div>

        <div class="form-actions">
          <button type="button" class="btn-secondary" onclick={closeModals}>Cancel</button>
          <button type="submit" class="btn-primary">Save</button>
        </div>
      </form>
    </div>
  </div>
{/if}

<style>
  .zone-list {
    max-width: 1000px;
  }

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 1rem;
  }

  h2 {
    margin: 0;
    font-size: 1.5rem;
  }

  .table {
    width: 100%;
    border-collapse: collapse;
    background: var(--table-bg, #fff);
    border-radius: 4px;
    overflow: hidden;
  }

  th, td {
    padding: 0.75rem 1rem;
    text-align: left;
    border-bottom: 1px solid var(--border-color, #eee);
  }

  th {
    background: var(--th-bg, #f5f5f5);
    font-weight: 500;
  }

  .endpoint {
    font-family: monospace;
    font-size: 0.875rem;
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  code {
    padding: 0.125rem 0.375rem;
    background: var(--code-bg, #e8e8e8);
    border-radius: 3px;
    font-size: 0.875rem;
  }

  .status-badge {
    display: inline-block;
    padding: 0.25rem 0.5rem;
    background: var(--status-color);
    color: #fff;
    border-radius: 3px;
    font-size: 0.75rem;
    text-transform: uppercase;
  }

  .actions {
    white-space: nowrap;
  }

  .btn-primary {
    padding: 0.5rem 1rem;
    background: #1976d2;
    color: #fff;
    border: none;
    border-radius: 4px;
    cursor: pointer;
  }

  .btn-primary:hover {
    background: #1565c0;
  }

  .btn-secondary {
    padding: 0.5rem 1rem;
    background: #e0e0e0;
    color: #333;
    border: none;
    border-radius: 4px;
    cursor: pointer;
  }

  .btn-small {
    padding: 0.25rem 0.5rem;
    background: #e0e0e0;
    border: none;
    border-radius: 3px;
    cursor: pointer;
    margin-right: 0.25rem;
    font-size: 0.8125rem;
  }

  .btn-small:hover {
    background: #d0d0d0;
  }

  .btn-danger {
    background: #ffcdd2;
    color: #c62828;
  }

  .btn-danger:hover {
    background: #ef9a9a;
  }

  .loading, .error, .empty {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: #c62828;
  }

  /* Modal styles */
  .modal-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .modal {
    background: var(--modal-bg, #fff);
    padding: 1.5rem;
    border-radius: 8px;
    min-width: 400px;
    max-width: 90vw;
  }

  .modal h3 {
    margin: 0 0 1rem;
  }

  .form-group {
    margin-bottom: 1rem;
  }

  .form-group label {
    display: block;
    margin-bottom: 0.25rem;
    font-weight: 500;
  }

  .form-group input,
  .form-group select {
    width: 100%;
    padding: 0.5rem;
    border: 1px solid var(--border-color, #ddd);
    border-radius: 4px;
    font-size: 1rem;
  }

  .form-group input:disabled {
    background: var(--disabled-bg, #f5f5f5);
    cursor: not-allowed;
  }

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 1.5rem;
  }

  .form-error {
    color: #c62828;
    background: #ffcdd2;
    padding: 0.5rem;
    border-radius: 4px;
    margin-bottom: 1rem;
  }

  @media (prefers-color-scheme: dark) {
    .table {
      --table-bg: #2d2d2d;
      --border-color: #404040;
    }

    th {
      --th-bg: #333;
    }

    code {
      --code-bg: #404040;
    }

    .modal {
      --modal-bg: #2d2d2d;
      --border-color: #404040;
    }

    .form-group input:disabled {
      --disabled-bg: #404040;
    }
  }
</style>
