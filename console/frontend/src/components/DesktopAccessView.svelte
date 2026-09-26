<!--
  Qubes Air Console - MCP desktop consent.

  An MCP client that wants to read a qube's screen waits on the console until a
  person here allows or denies it. Allow sends a one-time grant to that waiting
  request, good for one frame within 30 seconds; the grant itself never reaches
  this page. Stop revokes a request or a live grant at any time, and ends a
  capture in progress.

  The server only serves this queue to a fleet-wide control session: a Bearer
  token cannot approve, and a zone-scoped session never sees the view.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import {
    approveDesktopAccessRequest,
    denyDesktopAccessRequest,
    listDesktopAccessRequests,
    listQubes,
    stopDesktopAccessRequest,
  } from '../lib/api';
  import type { DesktopAccessRequest } from '../lib/types';

  // Requests wait at most 30 seconds, so the queue is polled rather than
  // loaded once.
  const POLL_MS = 3000;

  let requests = $state<DesktopAccessRequest[]>([]);
  let qubeNames = $state<Record<string, string>>({});
  let loading = $state(true);
  let refreshing = $state(false);
  // A failed load and a refused decision are kept apart: the poll that
  // follows a refused decision must not wipe the reason off the page.
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let notice = $state<string | null>(null);
  let actionId = $state<string | null>(null);

  async function refresh(): Promise<void> {
    if (refreshing) return;
    refreshing = true;
    try {
      requests = await listDesktopAccessRequests();
      loadError = null;
      await resolveNames(requests);
    } catch (cause) {
      loadError = cause instanceof Error ? cause.message : 'Could not load desktop requests';
    } finally {
      refreshing = false;
      loading = false;
    }
  }

  // Names are looked up only when the queue mentions a qube this page has not
  // seen, not on every poll.
  async function resolveNames(queue: DesktopAccessRequest[]): Promise<void> {
    if (queue.every((request) => request.qube_id in qubeNames)) return;
    try {
      const { qubes } = await listQubes();
      qubeNames = Object.fromEntries(qubes.map((qube) => [qube.id, qube.name]));
    } catch {
      // The queue stays usable with ids alone.
    }
  }

  async function decide(request: DesktopAccessRequest, action: 'approve' | 'deny' | 'stop'): Promise<void> {
    if (actionId) return;
    actionId = request.id;
    notice = null;
    actionError = null;
    try {
      if (action === 'approve') {
        await approveDesktopAccessRequest(request.id);
        notice = `Allowed. ${request.subject} can read one frame of ${qubeName(request)} within 30 seconds.`;
      } else if (action === 'deny') {
        await denyDesktopAccessRequest(request.id);
        notice = 'Request denied.';
      } else {
        await stopDesktopAccessRequest(request.id);
        notice = 'Desktop access stopped.';
      }
    } catch (cause) {
      actionError = cause instanceof Error ? cause.message : 'The action could not be completed';
    } finally {
      actionId = null;
    }
    await refresh();
  }

  function qubeName(request: DesktopAccessRequest): string {
    return qubeNames[request.qube_id] ?? request.qube_id;
  }

  function expiresAt(request: DesktopAccessRequest): string {
    return new Date(request.expires_at).toLocaleTimeString();
  }

  onMount(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), POLL_MS);
    return () => window.clearInterval(timer);
  });
</script>

<section class="desktop-access" aria-labelledby="desktop-access-title">
  <div class="head">
    <div>
      <h2 id="desktop-access-title">Desktop access</h2>
      <p>MCP clients asking to read one frame of a qube's screen. Nothing is shown to them until you allow it.</p>
    </div>
    <button class="ghost" type="button" onclick={() => void refresh()} disabled={refreshing}>
      {refreshing ? 'Refreshing…' : 'Refresh'}
    </button>
  </div>

  {#if loadError}
    <p class="message error" role="alert">{loadError}</p>
  {/if}
  {#if actionError}
    <p class="message error" role="alert">{actionError}</p>
  {/if}
  {#if notice}
    <p class="message success" role="status">{notice}</p>
  {/if}

  {#if loading}
    <p class="empty">Loading desktop requests…</p>
  {:else if requests.length === 0 && !loadError}
    <p class="empty">No request is waiting and no desktop grant is live.</p>
  {:else if requests.length > 0}
    <ul class="requests">
      {#each requests as request (request.id)}
        <li class="request">
          <div class="copy">
            <div class="title">
              <h3>Read screen from {qubeName(request)}</h3>
              <span class="state" class:live={request.state !== 'pending'}>{request.state}</span>
            </div>
            <dl>
              <div><dt>Requested by</dt><dd>{request.subject}</dd></div>
              <div><dt>Qube ID</dt><dd class="id">{request.qube_id}</dd></div>
              <div><dt>{request.state === 'pending' ? 'Decide by' : 'Grant ends'}</dt><dd>{expiresAt(request)}</dd></div>
            </dl>
          </div>
          <div class="actions">
            {#if request.state === 'pending'}
              <button class="allow" type="button" aria-label={`Allow reading the screen of ${qubeName(request)}`}
                disabled={actionId !== null} onclick={() => void decide(request, 'approve')}>
                {actionId === request.id ? 'Working…' : 'Allow'}
              </button>
              <button class="ghost" type="button" aria-label={`Deny the screen request for ${qubeName(request)}`}
                disabled={actionId !== null} onclick={() => void decide(request, 'deny')}>
                Deny
              </button>
            {:else}
              <button class="stop" type="button" aria-label={`Stop desktop access to ${qubeName(request)}`}
                disabled={actionId !== null} onclick={() => void decide(request, 'stop')}>
                {actionId === request.id ? 'Stopping…' : 'Stop access'}
              </button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .desktop-access { color: var(--systemPrimary); }
  .head { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; margin-bottom: 0.9rem; }
  h2 { margin: 0; font: var(--title-1-emphasized); }
  .head p { margin: 0.35rem 0 0; font: var(--body); color: var(--systemSecondary); }
  .ghost {
    border: 1px solid var(--systemQuaternary); background: var(--pageBG); color: var(--systemPrimary);
    border-radius: var(--global-border-radius-xsmall); padding: 0.35rem 0.7rem; font: var(--callout); cursor: pointer;
  }
  .message { margin: 0 0 1rem; padding: 0.6rem 0.8rem; border-radius: var(--global-border-radius-xsmall); font: var(--body); }
  .message.error { border: 1px solid var(--systemRed); color: var(--systemRed); }
  .message.success { border: 1px solid var(--systemGreen); color: var(--systemPrimary); }
  .empty { margin: 0; font: var(--body); color: var(--systemSecondary); }

  .requests { list-style: none; margin: 0; padding: 0; display: grid; gap: 0.75rem; }
  .request {
    display: flex; align-items: center; justify-content: space-between; gap: 1.25rem; padding: 0.9rem 1rem;
    border: 1px solid var(--systemQuaternary); border-radius: var(--global-border-radius-small); background: var(--pageBG);
  }
  .copy { min-width: 0; }
  .title { display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem; }
  h3 { margin: 0; font: var(--headline-emphasized); }
  .state {
    padding: 0.1rem 0.5rem; border-radius: 999px; background: var(--systemQuinary);
    color: var(--systemSecondary); font: var(--callout); text-transform: capitalize;
  }
  .state.live { color: var(--systemGreen); }
  dl { display: flex; flex-wrap: wrap; gap: 0.3rem 1.5rem; margin: 0.55rem 0 0; }
  dl div { display: flex; gap: 0.35rem; font: var(--callout); }
  dt { color: var(--systemSecondary); }
  dd { margin: 0; }
  .id { overflow-wrap: anywhere; }
  .actions { display: flex; flex-shrink: 0; gap: 0.5rem; }
  .actions button { border-radius: var(--global-border-radius-xsmall); padding: 0.4rem 0.8rem; font: var(--callout-emphasized); cursor: pointer; }
  .actions button:disabled { cursor: wait; opacity: 0.6; }
  .allow { border: 1px solid var(--systemGreen); background: var(--systemGreen); color: var(--pageBG); }
  .stop { border: 1px solid var(--systemRed); background: color-mix(in srgb, var(--systemRed) 10%, var(--pageBG)); color: var(--systemRed); }

  @media (max-width: 700px) {
    .request { flex-direction: column; align-items: stretch; }
    .actions > button { flex: 1; }
  }
</style>
