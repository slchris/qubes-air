<!--
  Qubes Air Console - Qube List Component

  Displays qubes with CRUD operations and lifecycle management.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { qubeStore, zoneStore } from '../lib/stores';
  import type { Zone, Qube, QubeType, QubeCreateRequest } from '../lib/types';
  import { ApiException } from '../lib/api';

  // Subscribe to stores
  let qubeState = $state({ qubes: [] as Qube[], loading: false, error: null as string | null });
  let zoneState = $state({ zones: [] as Zone[], loading: false, error: null as string | null });
  
  $effect(() => {
    const unsubQubes = qubeStore.subscribe(state => { qubeState = state; });
    const unsubZones = zoneStore.subscribe(state => { zoneState = state; });
    return () => { unsubQubes(); unsubZones(); };
  });

  // Derived: connected zones
  let connectedZonesList = $derived(zoneState.zones.filter(z => z.status === 'connected'));

  // Modal state
  let showCreateModal = $state(false);
  let showEditModal = $state(false);
  let editingQube = $state<Qube | null>(null);
  let actionError = $state<string | null>(null);
  let processing = $state<string | null>(null);

  // Form data
  let formName = $state('');
  let formZoneId = $state('');
  let formType = $state<QubeType>('work');
  let formVcpu = $state(2);
  let formMemory = $state(2048);
  let formDisk = $state(20);
  let formTemplate = $state('debian-12');

  const qubeTypes: QubeType[] = ['app', 'work', 'dev', 'gpu', 'disp', 'sys'];

  onMount(async () => {
    await Promise.all([qubeStore.load(), zoneStore.load()]);
  });

  function getStatusColor(status: string): string {
    switch (status) {
      case 'running': return '#4caf50';
      case 'stopped': return '#9e9e9e';
      case 'error': return '#f44336';
      case 'creating': return '#2196f3';
      default: return '#ff9800';
    }
  }

  function resetForm(): void {
    formName = '';
    formZoneId = connectedZonesList[0]?.id ?? '';
    formType = 'work';
    formVcpu = 2;
    formMemory = 2048;
    formDisk = 20;
    formTemplate = 'debian-12';
    actionError = null;
  }

  function openCreateModal(): void {
    resetForm();
    showCreateModal = true;
  }

  function openEditModal(qube: Qube): void {
    editingQube = qube;
    formName = qube.name;
    formVcpu = qube.spec.vcpu;
    formMemory = qube.spec.memory;
    formDisk = qube.spec.disk;
    formTemplate = qube.spec.template;
    actionError = null;
    showEditModal = true;
  }

  function closeModals(): void {
    showCreateModal = false;
    showEditModal = false;
    editingQube = null;
    resetForm();
  }

  async function handleCreate(): Promise<void> {
    actionError = null;
    const data: QubeCreateRequest = {
      name: formName.trim(),
      type: formType,
      spec: {
        vcpu: formVcpu,
        memory: formMemory,
        disk: formDisk,
        template: formTemplate,
      },
    };

    // Only include zone_id if one is selected
    if (formZoneId) {
      data.zone_id = formZoneId;
    }

    try {
      await qubeStore.create(data);
      closeModals();
    } catch (e) {
      actionError = e instanceof ApiException ? e.message : 'Failed to create qube';
    }
  }

  async function handleUpdate(): Promise<void> {
    if (!editingQube) return;
    actionError = null;

    try {
      await qubeStore.updateQube(editingQube.id, {
        name: formName.trim(),
        spec: {
          vcpu: formVcpu,
          memory: formMemory,
          disk: formDisk,
          template: formTemplate,
        },
      });
      closeModals();
    } catch (e) {
      actionError = e instanceof ApiException ? e.message : 'Failed to update qube';
    }
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
    processing = qube.id;
    try {
      await qubeStore.start(qube.id);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to start qube');
    } finally {
      processing = null;
    }
  }

  async function handleStop(qube: Qube): Promise<void> {
    processing = qube.id;
    try {
      await qubeStore.stop(qube.id);
    } catch (e) {
      alert(e instanceof ApiException ? e.message : 'Failed to stop qube');
    } finally {
      processing = null;
    }
  }

  function getZoneName(zoneId: string): string {
    if (!zoneId) return 'No Zone';
    return zoneState.zones.find(z => z.id === zoneId)?.name ?? 'Unknown';
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
    <div class="qube-grid">
      {#each qubeState.qubes as qube (qube.id)}
        <div class="qube-card">
          <div class="qube-header">
            <span class="qube-name">{qube.name}</span>
            <span
              class="status-dot"
              style="background: {getStatusColor(qube.status)}"
              title={qube.status}
            ></span>
          </div>

          <div class="qube-info">
            <div class="info-row">
              <span class="label">Zone:</span>
              <span>{getZoneName(qube.zone_id)}</span>
            </div>
            <div class="info-row">
              <span class="label">Type:</span>
              <code>{qube.type}</code>
            </div>
            <div class="info-row">
              <span class="label">Spec:</span>
              <span>{qube.spec.vcpu} vCPU, {qube.spec.memory}MB</span>
            </div>
            {#if qube.ip_address}
              <div class="info-row">
                <span class="label">IP:</span>
                <code>{qube.ip_address}</code>
              </div>
            {/if}
          </div>

          <div class="qube-actions">
            {#if processing === qube.id}
              <button class="btn" disabled>Processing...</button>
            {:else if qube.status === 'stopped'}
              <button class="btn" onclick={() => handleStart(qube)}>Start</button>
            {:else if qube.status === 'running'}
              <button class="btn" onclick={() => handleStop(qube)}>Stop</button>
            {:else}
              <button class="btn" disabled>{qube.status}</button>
            {/if}
            <button class="btn btn-secondary" onclick={() => openEditModal(qube)}>Edit</button>
            <button
              class="btn btn-danger"
              onclick={() => handleDelete(qube)}
              disabled={qube.status === 'running'}
            >
              Delete
            </button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Create Modal -->
{#if showCreateModal}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal-overlay" onclick={closeModals}>
    <div class="modal" onclick={stopPropagation}>
      <h3>Create Qube</h3>

      {#if actionError}
        <p class="form-error">{actionError}</p>
      {/if}

      <form onsubmit={handleSubmitCreate}>
        <div class="form-group">
          <label for="name">Name</label>
          <input id="name" type="text" bind:value={formName} required />
        </div>

        <div class="form-group">
          <label for="zone">Zone (Optional)</label>
          <select id="zone" bind:value={formZoneId}>
            <option value="">-- No Zone --</option>
            {#each connectedZonesList as zone}
              <option value={zone.id}>{zone.name} ({zone.type})</option>
            {/each}
          </select>
        </div>

        <div class="form-group">
          <label for="type">Type</label>
          <select id="type" bind:value={formType}>
            {#each qubeTypes as t}
              <option value={t}>{t}</option>
            {/each}
          </select>
        </div>

        <div class="form-row">
          <div class="form-group">
            <label for="vcpu">vCPU</label>
            <input id="vcpu" type="number" bind:value={formVcpu} min="1" max="32" />
          </div>

          <div class="form-group">
            <label for="memory">Memory (MB)</label>
            <input id="memory" type="number" bind:value={formMemory} min="512" step="512" />
          </div>
        </div>

        <div class="form-row">
          <div class="form-group">
            <label for="disk">Disk (GB)</label>
            <input id="disk" type="number" bind:value={formDisk} min="10" />
          </div>

          <div class="form-group">
            <label for="template">Template</label>
            <input id="template" type="text" bind:value={formTemplate} />
          </div>
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
{#if showEditModal && editingQube}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal-overlay" onclick={closeModals}>
    <div class="modal" onclick={stopPropagation}>
      <h3>Edit Qube</h3>

      {#if actionError}
        <p class="form-error">{actionError}</p>
      {/if}

      <form onsubmit={handleSubmitUpdate}>
        <div class="form-group">
          <label for="edit-name">Name</label>
          <input id="edit-name" type="text" bind:value={formName} required />
        </div>

        <div class="form-group">
          <label for="edit-zone">Zone</label>
          <input id="edit-zone" type="text" value={getZoneName(editingQube.zone_id)} disabled />
        </div>

        <div class="form-group">
          <label for="edit-qube-type">Type</label>
          <input id="edit-qube-type" type="text" value={editingQube.type} disabled />
        </div>

        <div class="form-row">
          <div class="form-group">
            <label for="edit-vcpu">vCPU</label>
            <input id="edit-vcpu" type="number" bind:value={formVcpu} min="1" max="32" />
          </div>

          <div class="form-group">
            <label for="edit-memory">Memory (MB)</label>
            <input id="edit-memory" type="number" bind:value={formMemory} min="512" step="512" />
          </div>
        </div>

        <div class="form-row">
          <div class="form-group">
            <label for="edit-disk">Disk (GB)</label>
            <input id="edit-disk" type="number" bind:value={formDisk} min="10" />
          </div>

          <div class="form-group">
            <label for="edit-template">Template</label>
            <input id="edit-template" type="text" bind:value={formTemplate} />
          </div>
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
  .qube-list {
    max-width: 1200px;
  }

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 1rem;
  }

  h2 {
    margin: 0;
  }

  .qube-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: 1rem;
  }

  .qube-card {
    background: var(--card-bg, #fff);
    border: 1px solid var(--border-color, #ddd);
    border-radius: 6px;
    padding: 1rem;
  }

  .qube-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.75rem;
  }

  .qube-name {
    font-weight: 600;
    font-size: 1.125rem;
  }

  .status-dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
  }

  .qube-info {
    margin-bottom: 1rem;
  }

  .info-row {
    display: flex;
    gap: 0.5rem;
    font-size: 0.875rem;
    margin-bottom: 0.25rem;
  }

  .label {
    color: var(--label-color, #666);
    min-width: 50px;
  }

  code {
    padding: 0.125rem 0.375rem;
    background: var(--code-bg, #e8e8e8);
    border-radius: 3px;
    font-size: 0.8125rem;
  }

  .qube-actions {
    display: flex;
    gap: 0.5rem;
  }

  .btn {
    flex: 1;
    padding: 0.5rem;
    background: #1976d2;
    color: #fff;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.875rem;
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

  .btn-primary {
    padding: 0.5rem 1rem;
    background: #1976d2;
    color: #fff;
    border: none;
    border-radius: 4px;
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
    font-size: 0.875rem;
    margin-top: 0.5rem;
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
    min-width: 450px;
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

  .form-row {
    display: flex;
    gap: 1rem;
  }

  .form-row .form-group {
    flex: 1;
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
    .qube-card {
      --card-bg: #2d2d2d;
      --border-color: #404040;
    }

    .label {
      --label-color: #aaa;
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

    .hint {
      --label-color: #aaa;
    }
  }
</style>
