<!--
  Qubes Air Console - authentication gate.

  Shown instead of the console when no API token is stored, or when the server
  rejected the one that is. There is no login endpoint: the console
  authenticates with a single static bearer token that the operator reads out of
  the console qube. So this screen's real job is to say WHERE that token is,
  not to collect a username and password that do not exist.
-->
<script lang="ts">
  import { setApiToken } from '../lib/api';
  import { auth } from '../lib/auth.svelte';

  let token = $state('');
  let revealed = $state(false);

  function submit(event: Event): void {
    event.preventDefault();
    const trimmed = token.trim();
    if (!trimmed) return;
    // setApiToken notifies the gate, which re-evaluates and lets the app render.
    setApiToken(trimmed);
    token = '';
  }
</script>

<div class="gate">
  <form class="card" onsubmit={submit}>
    <h1>Qubes Air Console</h1>

    {#if auth.wasRejected}
      <p class="alert error">
        The server rejected this token. It may have been rotated, mistyped, or
        copied from a different deployment.
      </p>
    {:else}
      <p class="lede">
        This console authenticates with an API token. Paste it once; it is
        stored in this browser only.
      </p>
    {/if}

    <label class="field">
      <span>API token</span>
      <input
        type={revealed ? 'text' : 'password'}
        bind:value={token}
        placeholder="QUBES_AIR_API_TOKEN"
        autocomplete="off"
        spellcheck="false"
      />
    </label>

    <label class="reveal">
      <input type="checkbox" bind:checked={revealed} />
      <span>Show token</span>
    </label>

    <button type="submit" disabled={!token.trim()}>Unlock console</button>

    <details>
      <summary>Where do I find it?</summary>
      <p>
        The token is generated once when the console is deployed and is never
        transmitted anywhere. Read it in <strong>dom0</strong>:
      </p>
      <pre>qvm-run --pass-io -u root qubesair-console \
    'grep QUBES_AIR_API_TOKEN /rw/config/qubesair/secrets.env'</pre>
      <p class="muted">
        It is not filled in automatically on purpose: anything that put the
        token where this page could read it by itself would also hand it to
        anyone who can open this page.
      </p>
    </details>
  </form>
</div>

<style>
  .gate {
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 2rem 1rem;
    background: var(--bg, #f5f5f5);
  }

  .card {
    width: 100%;
    max-width: 28rem;
    background: var(--surface, #fff);
    border: 1px solid var(--border, #d4d4d4);
    border-radius: 6px;
    padding: 1.75rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  h1 {
    margin: 0;
    font-size: 1.25rem;
    font-weight: 600;
  }

  .lede,
  .alert {
    margin: 0;
    font-size: 0.9rem;
    line-height: 1.5;
  }

  .alert.error {
    padding: 0.65rem 0.8rem;
    border-radius: 4px;
    border: 1px solid #d97706;
    background: #fef3c7;
    color: #7c2d12;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    font-size: 0.85rem;
  }

  .field input {
    padding: 0.55rem 0.65rem;
    border: 1px solid var(--border, #d4d4d4);
    border-radius: 4px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.9rem;
  }

  .reveal {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.85rem;
  }

  button {
    padding: 0.6rem 1rem;
    border: 1px solid transparent;
    border-radius: 4px;
    background: var(--accent, #1d4ed8);
    color: #fff;
    font-size: 0.9rem;
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  details {
    font-size: 0.85rem;
    line-height: 1.5;
  }

  summary {
    cursor: pointer;
  }

  pre {
    overflow-x: auto;
    padding: 0.6rem;
    border-radius: 4px;
    background: #1f2937;
    color: #e5e7eb;
    font-size: 0.78rem;
  }

  .muted {
    color: #52525b;
  }
</style>
