<!--
  Qubes Air Console - Qube List Component

  Displays qubes with CRUD operations and lifecycle management.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { qubeStore, zoneStore } from '../lib/stores';
  import type { Zone, Qube, QubeType, QubeCreateRequest } from '../lib/types';
  import { isTransientStatus, nodeCanFit, SCHEDULER_HEADROOM } from '../lib/types';
  import type { NodeInfo, CapacityKind, QuotaInfo } from '../lib/types';
  import { getZoneCapacity } from '../lib/api';
  import { ApiException } from '../lib/api';
  import type { AgentHealth } from '../lib/types';
  import JobLog from './JobLog.svelte';

  // The agent-health label. "running + agent unhealthy" is the case worth
  // spelling out — a green status dot for a qube whose agent cannot be reached
  // is the failure this field exists to surface.
  function agentLabel(h: AgentHealth | undefined): string {
    switch (h) {
      case 'healthy': return 'healthy';
      case 'unhealthy': return 'unreachable';
      default: return 'unknown';
    }
  }

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
  // The OS image is a property of the ZONE (its template VM), not of a qube —
  // a per-qube template field existed here but the backend never consumed it.
  let formDataDisk = $state(20);
  let formNode = $state('');

  /**
   * Live cluster capacity for the selected zone.
   *
   * Shown so "automatic" is an informed choice. Offering a node field without
   * capacity numbers asks the operator to guess, which is how everything ends
   * up on whichever node happened to be the default.
   */
  let nodes = $state<NodeInfo[]>([]);
  let capacityKind = $state<CapacityKind | null>(null);
  let quota = $state<QuotaInfo | null>(null);
  let capacityNote = $state('');
  let nodesError = $state<string | null>(null);
  let loadingNodes = $state(false);

  /** Fetches capacity for a zone, degrading quietly when unavailable. */
  async function loadNodes(zoneId: string): Promise<void> {
    nodes = [];
    quota = null;
    capacityKind = null;
    capacityNote = '';
    nodesError = null;
    if (!zoneId) return;
    loadingNodes = true;
    try {
      const cap = await getZoneCapacity(zoneId);
      capacityKind = cap.kind;
      capacityNote = cap.note ?? '';
      nodes = cap.nodes ?? [];
      quota = cap.quota ?? null;
    } catch (e) {
      // 503 (unreachable / no credential) and 501 (no scheduler) are expected.
      // The node field stays usable as free text.
      nodesError = e instanceof ApiException ? e.message : 'Capacity unavailable';
    } finally {
      loadingNodes = false;
    }
  }

  /**
   * Node selection only applies to a finite node pool. On an elastic provider
   * the cloud picks the machine and never reports which, so offering a node
   * field there would be asking for something that has no effect.
   */
  let showNodePicker = $derived(capacityKind === 'node_pool' || nodesError !== null);

  /** The node automatic placement would choose for the current form values. */
  let autoPick = $derived.by(() => {
    const eligible = nodes.filter(n => nodeCanFit(n, formMemory));
    if (eligible.length === 0) return null;
    return eligible.reduce((best, n) =>
      n.mem_free_bytes > best.mem_free_bytes ? n : best);
  });

  function formatGiB(bytes: number): string {
    return (bytes / 1024 ** 3).toFixed(1);
  }

  const qubeTypes: QubeType[] = ['app', 'work', 'dev', 'gpu', 'disp', 'sys'];

  onMount(async () => {
    await Promise.all([qubeStore.load(), zoneStore.load()]);
    // A reload lands mid-operation often enough to matter: an apply runs for
    // minutes, so pick the watches back up rather than showing a frozen status.
    qubeStore.resumeWatches();
  });

  function getStatusColor(status: string): string {
    switch (status) {
      case 'running': return '#4caf50';
      case 'stopped': return '#9e9e9e';
      // Suspended and released both mean "compute gone, data disk kept". They
      // are shown distinctly from stopped because they are the cheap state the
      // whole compute/storage separation exists to provide.
      case 'suspended': return '#7e57c2';
      case 'released': return '#616161';
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
      case 'deleting': return 'Releasing…';
      case 'suspended': return 'Suspended (data kept)';
      case 'released': return 'Released (data kept)';
      default: return status;
    }
  }

  /** A qube can be resumed from any state where its compute is not running. */
  function canStart(status: string): boolean {
    return status === 'stopped' || status === 'suspended'
      || status === 'released' || status === 'error';
  }

  function resetForm(): void {
    formName = '';
    formZoneId = connectedZonesList[0]?.id ?? '';
    void loadNodes(formZoneId);
    formType = 'work';
    formVcpu = 2;
    formMemory = 2048;
    formDisk = 20;
    formDataDisk = 20;
    formNode = '';
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
    formDataDisk = qube.spec.data_disk_gb ?? 20;
    formNode = qube.spec.node ?? '';
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
        data_disk_gb: formDataDisk,
        node: formNode || undefined,
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
          data_disk_gb: formDataDisk,
          node: formNode || undefined,
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
              title={getStatusLabel(qube.status)}
            ></span>
          </div>

          <div class="qube-info">
            <div class="info-row">
              <span class="label">Zone:</span>
              <span>{getZoneName(qube.zone_id)}{#if qube.spec.node} · {qube.spec.node}{/if}</span>
            </div>
            <div class="info-row">
              <span class="label">Type:</span>
              <code>{qube.type}</code>
            </div>
            <div class="info-row">
              <span class="label">Spec:</span>
              <span>{qube.spec.vcpu} vCPU, {qube.spec.memory}MB, {qube.spec.disk}G{#if qube.spec.data_disk_gb} +{qube.spec.data_disk_gb}G data{/if}</span>
            </div>
            {#if qube.ip_address}
              <div class="info-row">
                <span class="label">IP:</span>
                <code>{qube.ip_address}</code>
              </div>
            {/if}
            <div class="info-row">
              <span class="label">Agent:</span>
              <span class="agent {qube.agent_health ?? 'unknown'}" title={qube.agent_last_error || ''}>
                {agentLabel(qube.agent_health)}
              </span>
            </div>
            {#if qube.agent_health === 'unhealthy' && qube.agent_last_error}
              <div class="info-row agent-err">
                <span class="label"></span>
                <span title={qube.agent_last_error}>{qube.agent_last_error}</span>
              </div>
            {/if}
          </div>

          <!-- The provisioning/failure story. Shown while the qube is in a
               transient state, or whenever we have a job id for it (so a
               finished failure keeps its reason). -->
          {#if qubeState.jobs[qube.id]}
            <JobLog jobId={qubeState.jobs[qube.id]} active={isTransientStatus(qube.status)} />
          {/if}

          <div class="qube-actions">
            {#if isTransientStatus(qube.status)}
              <!-- An operation is in flight. The backend refuses a second one,
                   so the buttons are disabled rather than offering a click that
                   would come back 409. -->
              <button class="btn" disabled>{getStatusLabel(qube.status)}</button>
            {:else if canStart(qube.status)}
              <button class="btn" onclick={() => handleStart(qube)}>
                {qube.status === 'suspended' || qube.status === 'released' ? 'Resume' : 'Start'}
              </button>
            {:else if qube.status === 'running'}
              <button class="btn" onclick={() => handleStop(qube)} title="Destroy the compute instance and keep the data disk">
                Suspend
              </button>
            {:else}
              <button class="btn" disabled>{getStatusLabel(qube.status)}</button>
            {/if}
            <button
              class="btn btn-secondary"
              onclick={() => openEditModal(qube)}
              disabled={isTransientStatus(qube.status)}
            >Edit</button>
            <button
              class="btn btn-danger"
              onclick={() => handleDelete(qube)}
              disabled={isTransientStatus(qube.status) || qube.status === 'released'}
              title="Release the compute instance. The data disk is kept and can be purged separately."
            >
              Release
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
          <select id="zone" bind:value={formZoneId} onchange={() => void loadNodes(formZoneId)}>
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
            <label for="data-disk">Data disk (GB)</label>
            <input id="data-disk" type="number" bind:value={formDataDisk} min="1" />
            <small class="field-hint">
              Persistent. Survives suspend/resume; the OS disk does not.
            </small>
          </div>
          <div class="form-group">
            {#if !showNodePicker && capacityKind === 'quota'}
              <!-- Elastic provider: placement is the cloud's decision, so no
                   node field. What matters instead is usage against quota. -->
              <label for="quota-info">Placement</label>
              <p id="quota-info" class="field-hint">
                Handled by the provider — cloud zones have no node to choose.
                {#if quota}
                  Using {quota.vcpu_used}{quota.vcpu_limit ? ` of ${quota.vcpu_limit}` : ''} vCPU
                  across {quota.instances_used} instance{quota.instances_used === 1 ? '' : 's'}.
                  {#if quota.month_to_date_usd}${quota.month_to_date_usd.toFixed(2)} month to date.{/if}
                {:else if capacityNote}
                  {capacityNote}
                {/if}
              </p>
            {:else}
            <label for="node">Node</label>
            <select id="node" bind:value={formNode}>
              <option value="">
                Automatic{autoPick ? ` — would pick ${autoPick.name}` : ''}
              </option>
              {#each nodes as node}
                <option value={node.name} disabled={!nodeCanFit(node, formMemory)}>
                  {node.name} — {formatGiB(node.mem_free_bytes)} GiB free
                  {#if !node.online}(offline){:else if !nodeCanFit(node, formMemory)}(insufficient){/if}
                </option>
              {/each}
            </select>
            {#if loadingNodes}
              <small class="field-hint">Reading cluster capacity…</small>
            {:else if nodesError}
              <small class="field-hint">
                Capacity unavailable ({nodesError}). Leave blank to use the zone
                default, or type a node name.
              </small>
              <input type="text" bind:value={formNode} placeholder="zone default" />
            {:else if nodes.length > 0}
              <small class="field-hint">
                Automatic picks the node with the most free memory, keeping
                {Math.round(SCHEDULER_HEADROOM * 100)}% of each node in reserve —
                a node that looks free enough can still be refused.
              </small>
            {/if}
            {/if}
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
            <label for="edit-data-disk">Data disk (GB)</label>
            <input id="edit-data-disk" type="number" bind:value={formDataDisk} min="1" />
            <small class="field-hint">
              Growing this is applied on the next resume. Disks cannot shrink.
            </small>
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

  .agent { font-weight: 500; }
  .agent.healthy { color: #16a34a; }
  .agent.unhealthy { color: #dc2626; }
  .agent.unknown { color: #6b7280; }
  .agent-err span { color: #b91c1c; font-size: 0.78rem; word-break: break-word; }

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
    /* width, not min-width. In CSS min-width BEATS max-width, so
       `min-width: 450px; max-width: 90vw` kept the dialog 450px wide on any
       viewport narrower than that — the 90vw cap never applied and the dialog
       overflowed horizontally, which is what browser zoom produces (zooming in
       shrinks the CSS viewport). min() applies whichever is smaller. */
    width: min(450px, 90vw);
    max-height: 90dvh;
    overflow-y: auto;
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

  .field-hint {
    display: block;
    margin-top: 0.25rem;
    font-size: 0.75rem;
    color: var(--text-muted, #888);
    line-height: 1.4;
  }
</style>
