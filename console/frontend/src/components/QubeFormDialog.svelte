<!--
  Qubes Air Console - Qube create/edit dialog

  One form for both modes. Create picks a zone, a type and (on a node-pool
  provider) a node; edit shows the zone and type read-only, because the backend
  does not move a qube between zones or change its type.

  The number inputs' min/max are the API's own spec bounds
  (internal/service/specbounds.go, qube_spec.* in the config) so the form does
  not offer what the API refuses; config_test.go and QubeFormDialog.test.ts pin
  both sides.
-->
<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { ApiException, getZoneCapacity } from '../lib/api';
  import { qubeStore } from '../lib/stores';
  import { nodeCanFit, SCHEDULER_HEADROOM } from '../lib/types';
  import type {
    CapacityKind, NodeInfo, Qube, QubeCreateRequest, QubeType, QuotaInfo, Zone,
  } from '../lib/types';

  let {
    mode,
    qube = null,
    connectedZones,
    zones,
    onclose,
  }: {
    mode: 'create' | 'edit';
    qube?: Qube | null;
    connectedZones: Zone[];
    zones: Zone[];
    onclose: () => void;
  } = $props();

  // The form is seeded once, when the dialog opens. A qube row that refreshes
  // underneath an open edit (the status poll replaces the object) must not
  // overwrite what the operator has typed.
  const initialQube = untrack(() => qube);
  const initialZoneId = untrack(() => connectedZones[0]?.id ?? '');

  let formName = $state(initialQube?.name ?? '');
  let formZoneId = $state(initialZoneId);
  let formType = $state<QubeType>(initialQube?.type ?? 'work');
  let formVcpu = $state(initialQube?.spec.vcpu ?? 2);
  let formMemory = $state(initialQube?.spec.memory ?? 2048);
  let formDisk = $state(initialQube?.spec.disk ?? 20);
  // The OS image is a property of the ZONE (its template VM), not of a qube —
  // a per-qube template field existed here but the backend never consumed it.
  let formDataDisk = $state(initialQube?.spec.data_disk_gb ?? 20);
  let formNode = $state(initialQube?.spec.node ?? '');
  let actionError = $state<string | null>(null);

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

  const qubeTypes: QubeType[] = ['app', 'work', 'dev', 'gpu', 'disp', 'sys'];

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

  onMount(() => {
    // Placement is chosen at create time only; edit has no node field.
    if (mode === 'create') void loadNodes(formZoneId);
  });

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

  function formatGiB(bytes: number): string {
    return (bytes / 1024 ** 3).toFixed(1);
  }

  function getZoneName(zoneId: string | undefined): string {
    if (!zoneId) return 'No Zone';
    return zones.find(z => z.id === zoneId)?.name ?? 'Unknown';
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    actionError = null;
    const spec = {
      vcpu: formVcpu,
      memory: formMemory,
      disk: formDisk,
      data_disk_gb: formDataDisk,
      node: formNode || undefined,
    };
    try {
      if (mode === 'edit' && initialQube) {
        await qubeStore.updateQube(initialQube.id, { name: formName.trim(), spec });
      } else {
        const data: QubeCreateRequest = { name: formName.trim(), type: formType, spec };
        // Only include zone_id if one is selected
        if (formZoneId) data.zone_id = formZoneId;
        await qubeStore.create(data);
      }
      onclose();
    } catch (e) {
      // ApiException.message is the API's own reason (api.ts errorMessage), so a
      // refused spec bound reads as which bound, not as "Bad Request".
      actionError = e instanceof ApiException
        ? e.message
        : mode === 'edit' ? 'Failed to update qube' : 'Failed to create qube';
    }
  }
</script>

<div class="modal-layer">
  <button type="button" class="modal-backdrop" aria-label="Close qube dialog" onclick={onclose}></button>
  <div class="modal" role="dialog" aria-modal="true" aria-labelledby="qube-form-title" tabindex="-1">
    <h3 id="qube-form-title">{mode === 'create' ? 'Create Qube' : 'Edit Qube'}</h3>

    {#if actionError}
      <p class="form-error">{actionError}</p>
    {/if}

    <form onsubmit={submit}>
      <div class="form-group">
        <label for="qube-name">Name</label>
        <input id="qube-name" type="text" bind:value={formName} required />
      </div>

      {#if mode === 'create'}
        <div class="form-group">
          <label for="qube-zone">Zone (Optional)</label>
          <select id="qube-zone" bind:value={formZoneId} onchange={() => void loadNodes(formZoneId)}>
            <option value="">-- No Zone --</option>
            {#each connectedZones as zone}
              <option value={zone.id}>{zone.name} ({zone.type})</option>
            {/each}
          </select>
        </div>

        <div class="form-group">
          <label for="qube-type">Type</label>
          <select id="qube-type" bind:value={formType}>
            {#each qubeTypes as t}
              <option value={t}>{t}</option>
            {/each}
          </select>
        </div>
      {:else if initialQube}
        <div class="form-group">
          <label for="qube-zone-readonly">Zone</label>
          <input id="qube-zone-readonly" type="text" value={getZoneName(initialQube.zone_id)} disabled />
        </div>

        <div class="form-group">
          <label for="qube-type-readonly">Type</label>
          <input id="qube-type-readonly" type="text" value={initialQube.type} disabled />
        </div>
      {/if}

      <div class="form-row">
        <div class="form-group">
          <label for="qube-vcpu">vCPU</label>
          <input id="qube-vcpu" type="number" bind:value={formVcpu} min="1" max="32" />
        </div>

        <div class="form-group">
          <label for="qube-memory">Memory (MB)</label>
          <input id="qube-memory" type="number" bind:value={formMemory} min="512" step="512" />
        </div>
      </div>

      <div class="form-row">
        <div class="form-group">
          <label for="qube-disk">Disk (GB)</label>
          <input id="qube-disk" type="number" bind:value={formDisk} min="10" />
        </div>

        <div class="form-group">
          <label for="qube-data-disk">Data disk (GB)</label>
          <input id="qube-data-disk" type="number" bind:value={formDataDisk} min="1" />
          <small class="field-hint">
            {mode === 'create'
              ? 'Persistent. Survives suspend/resume; the OS disk does not.'
              : 'Growing this is applied on the next resume. Disks cannot shrink.'}
          </small>
        </div>

        {#if mode === 'create'}
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
              <label for="qube-node">Node</label>
              <select id="qube-node" bind:value={formNode}>
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
        {/if}
      </div>

      <div class="form-actions">
        <button type="button" class="btn-secondary" onclick={onclose}>Cancel</button>
        <button type="submit" class="btn-primary">{mode === 'create' ? 'Create' : 'Save'}</button>
      </div>
    </form>
  </div>
</div>

<style>
  .modal-layer {
    position: fixed;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .modal-backdrop {
    position: absolute;
    inset: 0;
    padding: 0;
    border: 0;
    background: rgba(0, 0, 0, 0.5);
    cursor: default;
  }

  .modal {
    position: relative;
    z-index: 1;
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
    border-radius: var(--global-border-radius-xsmall);
    font: var(--title-2-emphasized);
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
    min-width: 0;
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
    border-radius: var(--global-border-radius-xsmall);
    margin-bottom: 1rem;
  }

  .field-hint {
    display: block;
    margin-top: 0.25rem;
    font: var(--subhead);
    color: var(--text-muted, #888);
    line-height: 1.4;
  }

  .btn-primary {
    padding: 0.5rem 1rem;
    background: #1976d2;
    color: #fff;
    border: none;
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
  }

  .btn-secondary {
    padding: 0.5rem 1rem;
    background: #e0e0e0;
    color: #333;
    border: none;
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
  }

  @media (prefers-color-scheme: dark) {
    .modal {
      --modal-bg: #2d2d2d;
      --border-color: #404040;
    }

    .form-group input:disabled {
      --disabled-bg: #404040;
    }
  }
</style>
