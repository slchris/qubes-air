<!--
  Qubes Air Console - Zones.

  A zone is a cloud/hypervisor the console provisions into. This view is built
  around what a Proxmox zone actually carries — node, template, datastore, and
  the linked credential. Proxmox is the only provider with an adapter: the other
  types are listed but cannot be chosen, and the backend refuses to create them
  (see lib/providers.ts).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { zoneStore } from '../lib/stores';
  import type { Zone, ZoneType, ZoneCreateRequest } from '../lib/types';
  import { ApiException, apiFetch } from '../lib/api';
  import {
    DEFAULT_ZONE_PROVIDER, NOT_IMPLEMENTED, ZONE_PROVIDERS,
    isProviderImplemented, isUnimplementedProvider, providerOptionLabel,
    unimplementedProviderLabels,
  } from '../lib/providers';

  let zs = $state({ zones: [] as Zone[], loading: false, error: null as string | null });
  $effect(() => {
    const unsub = zoneStore.subscribe(s => { zs = s; });
    return unsub;
  });

  let busy = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  // ---- create form -------------------------------------------------------
  // A zone could only be created through the API before this, so standing up a
  // deployment meant reading the README with a terminal open. It is the first
  // thing an operator has to do and was the one thing the UI could not do.
  interface Cred { id: string; name: string; type: string }
  let creds = $state<Cred[]>([]);
  let credsError = $state<string | null>(null);

  let showCreate = $state(false);
  let saving = $state(false);
  let formError = $state<string | null>(null);

  let fName = $state('');
  let fType = $state<ZoneType>(DEFAULT_ZONE_PROVIDER);
  let fCredential = $state('');
  let fEndpoint = $state('');
  // proxmox
  let fNode = $state('');
  let fTemplateVmId = $state<number | null>(null);
  let fTemplateNode = $state('');
  let fDatastore = $state('');
  let fBridge = $state('vmbr0');

  // Only credentials of the zone's own type can authenticate it; offering the
  // rest invites a zone that connects to nothing.
  const usableCreds = $derived(creds.filter(c => c.type === fType));

  async function loadCreds(): Promise<void> {
    credsError = null;
    try {
      const r = await apiFetch('/credentials');
      if (!r.ok) throw new Error('Failed to load credentials');
      const d = await r.json();
      creds = d.credentials ?? [];
    } catch (e) {
      credsError = e instanceof Error ? e.message : 'Could not load credentials';
      creds = [];
    }
  }

  onMount(async () => { await Promise.all([zoneStore.load(), loadCreds()]); });

  function openCreate(): void {
    fName = ''; fType = DEFAULT_ZONE_PROVIDER; fCredential = ''; fEndpoint = '';
    fNode = ''; fTemplateVmId = null; fTemplateNode = ''; fDatastore = ''; fBridge = 'vmbr0';
    formError = null;
    showCreate = true;
    void loadCreds();
  }

  function buildRequest(): ZoneCreateRequest {
    const req: ZoneCreateRequest = {
      name: fName.trim(),
      type: fType,
      config: { endpoint: fEndpoint.trim() },
    };
    if (fType === 'proxmox') {
      req.config.proxmox = {
        node: fNode.trim() || undefined,
        template_vm_id: fTemplateVmId ?? undefined,
        template_node: fTemplateNode.trim() || undefined,
        datastore_id: fDatastore.trim() || undefined,
        network_bridge: fBridge.trim() || undefined,
        credential_id: fCredential || undefined,
      };
    }
    return req;
  }

  // Refuses the settings that produce a zone which looks configured and cannot
  // provision — the failures otherwise surface minutes into a provision job.
  function validate(): string | null {
    // The picker cannot select one, but the backend is not the only guard worth
    // having: say why here rather than round-tripping for a 422.
    if (!isProviderImplemented(fType)) return `${fType} is ${NOT_IMPLEMENTED}: this console has no adapter for it`;
    if (!fName.trim()) return 'Name is required';
    if (!fCredential) return 'A credential is required — the zone cannot reach its cluster without one';
    if (fType === 'proxmox') {
      if (!fEndpoint.trim()) return 'Endpoint is required';
      if (!fTemplateVmId) return 'Template VMID is required: without a template the clone produces a VM with no operating system';
    }
    return null;
  }

  async function submitCreate(e: Event): Promise<void> {
    e.preventDefault();
    formError = validate();
    if (formError) return;
    saving = true;
    try {
      await zoneStore.create(buildRequest());
      showCreate = false;
    } catch (err) {
      formError = err instanceof ApiException ? err.message : 'Failed to create zone';
    } finally {
      saving = false;
    }
  }

  async function toggle(zone: Zone): Promise<void> {
    busy = zone.id;
    actionError = null;
    try {
      if (zone.status === 'connected') {
        await zoneStore.disconnect(zone.id);
      } else {
        await zoneStore.connect(zone.id);
      }
    } catch (e) {
      // connect() reaches the cluster, so a bad credential or an unreachable
      // endpoint surfaces here — the one place the operator learns the zone is
      // misconfigured rather than merely "disconnected".
      actionError = e instanceof ApiException ? e.message : 'Action failed';
    } finally {
      busy = null;
    }
  }
</script>

<div class="zones">
  <div class="header">
    <h2>Zones</h2>
    <div class="head-actions">
      <button class="refresh" onclick={() => zoneStore.load()} disabled={zs.loading}>
        {zs.loading ? 'Loading…' : 'Refresh'}
      </button>
      <button class="primary" onclick={openCreate}>+ Add Zone</button>
    </div>
  </div>

  {#if actionError}
    <p class="banner error">{actionError}</p>
  {/if}

  {#if zs.error}
    <p class="banner error">{zs.error}</p>
  {:else if zs.zones.length === 0 && !zs.loading}
    <p class="empty">
      No zones yet. A zone is the cluster or project qubes are provisioned into;
      it needs a stored credential to reach it. Add one with <strong>+ Add Zone</strong>.
    </p>
  {:else}
    <div class="grid">
      {#each zs.zones as zone (zone.id)}
        <article class="card">
          <div class="card-head">
            <span class="name">{zone.name}</span>
            <span class="badge {zone.status}">{zone.status}</span>
          </div>

          <dl>
            <dt>Type</dt>
            <dd>
              <code>{zone.type}</code>
              {#if isUnimplementedProvider(zone.type)}
                <!-- A zone stored before the backend refused these types. It
                     stays listed, marked, so it is not mistaken for one that
                     can provision; the API can still delete it. -->
                <span class="unimpl">{NOT_IMPLEMENTED}</span>
              {/if}
            </dd>
            <dt>Endpoint</dt><dd class="mono">{zone.config.endpoint}</dd>
            {#if zone.config.proxmox}
              {@const p = zone.config.proxmox}
              {#if p.node}<dt>Default node</dt><dd>{p.node}</dd>{/if}
              {#if p.template_vm_id}
                <dt>Template</dt>
                <dd>VMID {p.template_vm_id}{#if p.template_node} on {p.template_node}{/if}</dd>
              {/if}
              {#if p.datastore_id}<dt>Datastore</dt><dd>{p.datastore_id}</dd>{/if}
              {#if p.network_bridge}<dt>Bridge</dt><dd>{p.network_bridge}</dd>{/if}
              <dt>Credential</dt>
              <dd>{p.credential_id ? 'linked' : '⚠ none — cannot provision'}</dd>
            {/if}
          </dl>

          <div class="actions">
            <button
              class="toggle"
              class:connected={zone.status === 'connected'}
              onclick={() => toggle(zone)}
              disabled={busy === zone.id}
            >
              {#if busy === zone.id}
                …
              {:else if zone.status === 'connected'}
                Disconnect
              {:else}
                Connect
              {/if}
            </button>
          </div>
        </article>
      {/each}
    </div>
  {/if}
</div>

{#if showCreate}
  <div class="scrim">
    <button type="button" class="scrim-backdrop" aria-label="Close add zone dialog" onclick={() => (showCreate = false)}></button>
    <div class="sheet" role="dialog" aria-modal="true" aria-labelledby="add-zone-title" tabindex="-1">
      <h3 id="add-zone-title">Add zone</h3>

      {#if formError}<p class="banner error">{formError}</p>{/if}

      <form onsubmit={submitCreate}>
        <label class="f">
          <span>Name</span>
          <input bind:value={fName} placeholder="infra" autocomplete="off" />
        </label>

        <label class="f">
          <span>Provider</span>
          <select bind:value={fType}>
            {#each ZONE_PROVIDERS as p (p.value)}
              <option value={p.value} disabled={!p.implemented}>{providerOptionLabel(p)}</option>
            {/each}
          </select>
        </label>
        {#if unimplementedProviderLabels().length > 0}
          <!-- Listed rather than hidden so an operator looking for them learns
               why they cannot be picked instead of assuming a missing option. -->
          <p class="note">
            {unimplementedProviderLabels().join(', ')}: {NOT_IMPLEMENTED}. This console
            has no provider adapter for them, so a zone of that type cannot be created.
          </p>
        {/if}

        <label class="f">
          <span>Credential</span>
          {#if credsError}
            <span class="note">{credsError}</span>
          {:else if usableCreds.length === 0}
            <span class="note">
              No {fType} credential stored. Add one in Credentials first — a zone
              without one cannot reach its cluster.
            </span>
          {:else}
            <select bind:value={fCredential}>
              <option value="">Select…</option>
              {#each usableCreds as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
            </select>
          {/if}
        </label>

        {#if fType === 'proxmox'}
          <label class="f">
            <span>API endpoint</span>
            <input bind:value={fEndpoint} placeholder="https://pve.example.com/" />
          </label>
          <div class="row">
            <label class="f">
              <span>Default node</span>
              <input bind:value={fNode} placeholder="node1" />
            </label>
            <label class="f">
              <span>Template VMID</span>
              <input type="number" bind:value={fTemplateVmId} placeholder="901" />
            </label>
          </div>
          <div class="row">
            <label class="f">
              <span>Template node</span>
              <input bind:value={fTemplateNode} placeholder="node the template lives on" />
            </label>
            <label class="f">
              <span>Datastore</span>
              <input bind:value={fDatastore} placeholder="ceph-pve" />
            </label>
          </div>
          <label class="f">
            <span>Network bridge</span>
            <input bind:value={fBridge} placeholder="vmbr0" />
          </label>
        {/if}

        <div class="actions">
          <button type="button" class="refresh" onclick={() => (showCreate = false)}>Cancel</button>
          <button type="submit" class="primary" disabled={saving}>
            {saving ? 'Creating…' : 'Create zone'}
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

<style>
  .head-actions { display: flex; gap: 8px; align-items: center; }
  .primary {
    padding: 6px 12px; border: 1px solid transparent;
    border-radius: var(--global-border-radius-xsmall);
    background: var(--keyColor); color: #fff; font: var(--body); cursor: pointer;
  }
  .primary:disabled { opacity: .5; cursor: not-allowed; }
  @media (hover: hover) and (pointer: fine) {
    .primary:hover { background: var(--keyColor-rollover); }
  }

  .scrim {
    position: fixed; inset: 0; z-index: 10001;
    display: grid; place-items: center; padding: 24px;
  }
  .scrim-backdrop {
    position: absolute; inset: 0; padding: 0; border: 0;
    background: var(--modalScrimColor); cursor: default;
  }
  .sheet {
    position: relative; z-index: 1;
    width: 100%; max-width: 560px; max-height: 85dvh; overflow-y: auto;
    background: var(--pageBG); color: var(--systemPrimary);
    border-radius: var(--modalBorderRadius);
    box-shadow: var(--shadow-medium);
    padding: 20px;
  }
  .sheet h3 { margin: 0 0 16px; font: var(--title-2-emphasized); }

  .f { display: flex; flex-direction: column; gap: 4px; margin-bottom: 12px; }
  .f > span { font: var(--callout); color: var(--systemSecondary); }
  .f input, .f select {
    padding: 6px 8px; font: var(--body);
    color: var(--systemPrimary); background: var(--pageBG);
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-xsmall);
  }
  .row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
  @media (max-width: 520px) { .row { grid-template-columns: 1fr; } }

  .note {
    margin: 0 0 12px; font: var(--callout); line-height: 1.45;
    color: var(--systemSecondary);
  }
  .unimpl {
    margin-left: 6px; padding: 0.1rem 0.4rem; border-radius: 999px;
    font: var(--subhead); color: var(--systemSecondary);
    border: 1px solid var(--systemQuaternary);
  }
  .banner.error {
    margin: 0 0 12px; padding: 8px 10px;
    border-radius: var(--global-border-radius-xsmall);
    border: 1px solid var(--systemRed);
    background: color-mix(in srgb, var(--systemRed) 10%, var(--pageBG));
    color: var(--systemPrimary); font: var(--body);
  }

  .actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }

  /* Same trap as LoginGate: a hard-coded light surface with no foreground of
     its own. Inside .app the inherited colour is LIGHT in dark mode, so these
     cards rendered light-on-white. Each surface sets both. */
  .zones {
    padding: 0.5rem;
    color: var(--systemPrimary);
  }

  .header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 1rem;
  }
  h2 { margin: 0; font: var(--title-2-emphasized); color: var(--systemPrimary); }
  .refresh {
    padding: 0.4rem 0.8rem; border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-xsmall); background: var(--pageBG); color: var(--systemPrimary);
    cursor: pointer;
  }
  .banner {
    margin: 0 0 1rem; padding: 0.6rem 0.8rem; border-radius: var(--global-border-radius-xsmall); font: var(--body);
  }
  .banner.error { border: 1px solid #d97706; background: #fef3c7; color: #7c2d12; }
  .empty { color: var(--systemSecondary); font: var(--body); line-height: 1.5; }

  .grid {
    display: grid; gap: 1rem;
    grid-template-columns: repeat(auto-fill, minmax(min(20rem, 100%), 1fr));
  }
  .card {
    border: 1px solid var(--systemQuaternary); border-radius: var(--global-border-radius-small);
    padding: 1rem; background: var(--pageBG); color: var(--systemPrimary);
  }
  .card-head {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.75rem;
  }
  .name { font-weight: 600; }
  .badge {
    font: var(--subhead); padding: 0.15rem 0.5rem; border-radius: 999px;
    text-transform: uppercase; letter-spacing: 0.03em;
  }
  .badge.connected { background: #dcfce7; color: #166534; }
  .badge.disconnected { background: #f3f4f6; color: #4b5563; }

  dl {
    display: grid; grid-template-columns: auto 1fr; gap: 0.3rem 0.75rem;
    margin: 0 0 1rem; font: var(--body);
  }
  dt { color: var(--systemSecondary); }
  dd { margin: 0; word-break: break-word; color: var(--systemPrimary); }
  .mono, code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font: var(--callout); }

  .actions { display: flex; justify-content: flex-end; }
  .toggle {
    padding: 0.45rem 0.9rem; border-radius: var(--global-border-radius-xsmall); border: 1px solid transparent;
    cursor: pointer; background: var(--keyColor); color: #fff; font: var(--body);
  }
  .toggle.connected { background: #fff; color: var(--systemRed); border-color: var(--systemRed); }
  .toggle:disabled { opacity: 0.5; cursor: not-allowed; }

</style>
