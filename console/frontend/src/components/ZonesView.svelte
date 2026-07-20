<!--
  Qubes Air Console - Zones.

  A zone is a cloud/hypervisor the console provisions into. The existing
  ZoneList.svelte was written for an aws/gcp shape (endpoint + region) and is not
  mounted anywhere; on a Proxmox deployment it showed nothing and its edit form
  would have erased the proxmox config on save. This view is built around what a
  Proxmox zone actually carries — node, template, datastore, and the linked
  credential — and preserves that config across an edit.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { zoneStore } from '../lib/stores';
  import type { Zone } from '../lib/types';
  import { ApiException } from '../lib/api';

  let zs = $state({ zones: [] as Zone[], loading: false, error: null as string | null });
  $effect(() => {
    const unsub = zoneStore.subscribe(s => { zs = s; });
    return unsub;
  });

  let busy = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  onMount(() => { zoneStore.load(); });

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
    <button class="refresh" onclick={() => zoneStore.load()} disabled={zs.loading}>
      {zs.loading ? 'Loading…' : 'Refresh'}
    </button>
  </div>

  {#if actionError}
    <p class="banner error">{actionError}</p>
  {/if}

  {#if zs.error}
    <p class="banner error">{zs.error}</p>
  {:else if zs.zones.length === 0 && !zs.loading}
    <p class="empty">
      No zones yet. A zone is registered against a credential — see the console
      README, "creating a zone".
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
            <dt>Type</dt><dd><code>{zone.type}</code></dd>
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

<style>
  /* Same trap as LoginGate: a hard-coded light surface with no foreground of
     its own. Inside .app the inherited colour is LIGHT in dark mode, so these
     cards rendered light-on-white. Each surface sets both. */
  .zones {
    --surface: #ffffff;
    --text: #1a1a1a;
    --muted: #6b7280;
    --border: #d4d4d4;
    --accent: #1d4ed8;
    padding: 0.5rem;
    color: var(--text);
  }

  @media (prefers-color-scheme: dark) {
    .zones {
      --surface: #1f2937;
      --text: #e5e7eb;
      --muted: #9ca3af;
      --border: #374151;
      --accent: #2563eb;
    }
  }
  .header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 1rem;
  }
  h2 { margin: 0; font-size: 1.2rem; color: var(--text); }
  .refresh {
    padding: 0.4rem 0.8rem; border: 1px solid var(--border);
    border-radius: 4px; background: var(--surface); color: var(--text);
    cursor: pointer;
  }
  .banner {
    margin: 0 0 1rem; padding: 0.6rem 0.8rem; border-radius: 4px; font-size: 0.9rem;
  }
  .banner.error { border: 1px solid #d97706; background: #fef3c7; color: #7c2d12; }
  .empty { color: var(--muted); font-size: 0.9rem; line-height: 1.5; }

  .grid {
    display: grid; gap: 1rem;
    grid-template-columns: repeat(auto-fill, minmax(min(20rem, 100%), 1fr));
  }
  .card {
    border: 1px solid var(--border); border-radius: 6px;
    padding: 1rem; background: var(--surface); color: var(--text);
  }
  .card-head {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 0.75rem;
  }
  .name { font-weight: 600; }
  .badge {
    font-size: 0.72rem; padding: 0.15rem 0.5rem; border-radius: 999px;
    text-transform: uppercase; letter-spacing: 0.03em;
  }
  .badge.connected { background: #dcfce7; color: #166534; }
  .badge.disconnected { background: #f3f4f6; color: #4b5563; }

  dl {
    display: grid; grid-template-columns: auto 1fr; gap: 0.3rem 0.75rem;
    margin: 0 0 1rem; font-size: 0.85rem;
  }
  dt { color: var(--muted); }
  dd { margin: 0; word-break: break-word; color: var(--text); }
  .mono, code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.82rem; }

  .actions { display: flex; justify-content: flex-end; }
  .toggle {
    padding: 0.45rem 0.9rem; border-radius: 4px; border: 1px solid transparent;
    cursor: pointer; background: var(--accent); color: #fff; font-size: 0.85rem;
  }
  .toggle.connected { background: #fff; color: #b91c1c; border-color: #b91c1c; }
  .toggle:disabled { opacity: 0.5; cursor: not-allowed; }

  @media (prefers-color-scheme: dark) {
    .toggle.connected { background: var(--surface); }
  }
</style>
