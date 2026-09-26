<!--
  Qubes Air Console - remote desktop applications

  Lists the apps a running remote qube reports (qubes.GetAppmenus) and starts one
  on request (qubes.StartApp). The menu text comes from inside the qube, so it is
  parsed by lib/appmenus.ts: names are shown as text, Exec lines are ignored, and
  only an allowlisted .desktop id is ever sent back.

  Requests are cancelled when the dialog closes, so a slow or unreachable qube
  cannot update a dialog the operator has already dismissed.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { ApiException, getQubeAppMenus, launchQubeApp } from '../lib/api';
  import { parseAppMenus, readLaunchReply } from '../lib/appmenus';
  import type { DesktopApp } from '../lib/appmenus';

  interface Props {
    qubeId: string;
    qubeName: string;
    onclose: () => void;
  }

  let { qubeId, qubeName, onclose }: Props = $props();
  let apps = $state<DesktopApp[]>([]);
  let loading = $state(true);
  let loadingError = $state<string | null>(null);
  let launchError = $state<string | null>(null);
  let launchNotice = $state<{ text: string; tone: 'success' | 'neutral' } | null>(null);
  let launchingId = $state<string | null>(null);
  let closeButton: HTMLButtonElement | undefined = $state();
  let menuController: AbortController | null = null;
  let launchController: AbortController | null = null;

  onMount(() => {
    closeButton?.focus();
    menuController = new AbortController();
    void loadApps(menuController);
    return cancelRequests;
  });

  async function loadApps(controller: AbortController): Promise<void> {
    try {
      const raw = await getQubeAppMenus(qubeId, controller.signal);
      if (!controller.signal.aborted) apps = parseAppMenus(raw);
    } catch (error) {
      if (!isAbortError(error)) loadingError = describeError(error, 'Could not read applications from this qube');
    } finally {
      if (!controller.signal.aborted) loading = false;
    }
  }

  async function launch(app: DesktopApp): Promise<void> {
    launchError = null;
    launchNotice = null;
    launchingId = app.id;
    const controller = new AbortController();
    launchController = controller;
    try {
      const outcome = readLaunchReply(await launchQubeApp(qubeId, app.id, controller.signal), app.id);
      if (controller.signal.aborted) return;
      // A 200 only means the request reached the qube; the remote service's
      // reply says whether the app started, and only its success line counts.
      if (outcome.result === 'launched') {
        launchNotice = { tone: 'success', text: `Launch request sent for ${app.name}. Check the remote desktop for its window.` };
      } else if (outcome.result === 'refused') {
        launchError = `${app.name} did not start: ${outcome.detail}`;
      } else {
        launchNotice = {
          tone: 'neutral',
          text: `Sent to the qube, but its reply was not recognised, so it is not known whether ${app.name} started: ${outcome.detail || '(empty reply)'}`,
        };
      }
    } catch (error) {
      if (!isAbortError(error)) launchError = describeError(error, `Could not start ${app.name}`);
    } finally {
      if (!controller.signal.aborted) {
        launchingId = null;
        launchController = null;
      }
    }
  }

  function cancelRequests(): void {
    menuController?.abort();
    launchController?.abort();
    menuController = null;
    launchController = null;
  }

  function close(): void {
    cancelRequests();
    onclose();
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') close();
  }

  function isAbortError(error: unknown): boolean {
    return error instanceof DOMException && error.name === 'AbortError';
  }

  // ApiException carries the console's own reason (api.ts errorMessage); any
  // other error is a network failure whose text is not worth showing raw.
  function describeError(error: unknown, fallback: string): string {
    return error instanceof ApiException ? error.message : fallback;
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<div class="modal-layer">
  <button type="button" class="modal-backdrop" aria-label="Close applications dialog" onclick={close}></button>
  <div class="modal" role="dialog" aria-modal="true" aria-labelledby="apps-title" tabindex="-1">
    <header class="dialog-header">
      <div>
        <h3 id="apps-title">Applications</h3>
        <p class="subtitle">Available in {qubeName}</p>
      </div>
      <button bind:this={closeButton} type="button" class="close-button" aria-label="Close applications" onclick={close}>×</button>
    </header>

    {#if loading}
      <p role="status" class="message">Reading application menu…</p>
    {:else if loadingError}
      <p role="alert" class="message error">{loadingError}</p>
    {:else if apps.length === 0}
      <p class="message">No launchable applications were reported by this qube.</p>
    {:else}
      <ul class="app-list" aria-label="Applications in {qubeName}">
        {#each apps as app (app.id)}
          <li>
            <div class="app-copy">
              <strong>{app.name}</strong>
              {#if app.comment}<span>{app.comment}</span>{/if}
            </div>
            <button type="button" class="launch-button" onclick={() => void launch(app)}
                    aria-label={launchingId === app.id ? `Starting ${app.name}` : `Launch ${app.name}`}
                    disabled={launchingId !== null}>
              {launchingId === app.id ? 'Starting…' : 'Launch'}
            </button>
          </li>
        {/each}
      </ul>
    {/if}

    {#if launchError}<p role="alert" class="message error">{launchError}</p>{/if}
    {#if launchNotice}<p role="status" class="message" class:success={launchNotice.tone === 'success'}>{launchNotice.text}</p>{/if}
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
    width: min(520px, 90vw);
    max-height: 85dvh;
    overflow-y: auto;
    padding: 1.25rem;
    border-radius: 8px;
    color: var(--systemPrimary);
    background: var(--modal-bg, #fff);
  }

  .dialog-header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: flex-start;
  }

  h3 {
    margin: 0;
  }

  .subtitle {
    margin: 0.25rem 0 1rem;
    color: var(--systemSecondary);
    font: var(--footnote);
  }

  .close-button {
    border: 0;
    background: transparent;
    color: var(--systemSecondary);
    font: var(--title-2-emphasized);
    cursor: pointer;
  }

  .app-list {
    display: grid;
    gap: 0.4rem;
    padding: 0;
    margin: 0;
    list-style: none;
  }

  .app-list li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.7rem;
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-small);
  }

  .app-copy {
    display: grid;
    min-width: 0;
    gap: 0.2rem;
  }

  /* Names and comments are the remote's text: wrap anything, never overflow. */
  .app-copy strong,
  .app-copy span {
    overflow-wrap: anywhere;
  }

  .app-copy span {
    color: var(--systemSecondary);
    font: var(--footnote);
  }

  .launch-button {
    flex: 0 0 auto;
    padding: 0.4rem 0.8rem;
    border: 0;
    border-radius: var(--global-border-radius-xsmall);
    color: #fff;
    background: #1976d2;
    cursor: pointer;
    font: var(--callout);
  }

  .launch-button:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  .message {
    margin: 0.8rem 0;
    font: var(--callout);
    overflow-wrap: anywhere;
  }

  .error {
    color: var(--systemRed);
  }

  .success {
    color: var(--systemGreen);
  }

  @media (prefers-color-scheme: dark) {
    .modal {
      --modal-bg: #2d2d2d;
    }
  }
</style>
