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
  /* Colours are declared for BOTH schemes, explicitly.
     This component replaces the whole .app shell, so it inherits none of the
     shell's colours — and with `color-scheme: light dark` in index.html the UA
     default text colour is WHITE on a dark-mode machine. A card with a
     hard-coded white background and no colour of its own therefore rendered
     white-on-white: the heading, the labels and the help text were all
     invisible, leaving only the placeholder and the button. Every surface here
     now sets its own foreground next to its background. */
  .gate {

    min-height: 100dvh;
    display: grid;
    place-items: center;
    padding: 2rem 1rem;
    background: var(--pageBg);
    color: var(--systemPrimary);
  }


  .card {
    width: 100%;
    max-width: 28rem;
    background: var(--pageBG);
    color: var(--systemPrimary);
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-small);
    padding: 1.75rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  h1 {
    margin: 0;
    font: var(--title-1-emphasized);
    font-weight: 600;
    color: var(--systemPrimary);
  }

  .lede {
    margin: 0;
    font: var(--body);
    line-height: 1.5;
    color: var(--systemSecondary);
  }

  .alert {
    margin: 0;
    font: var(--body);
    line-height: 1.5;
    padding: 0.65rem 0.8rem;
    border-radius: var(--global-border-radius-xsmall);
    border: 1px solid #d97706;
    background: #fef3c7;
    /* Fixed dark text: this banner keeps its amber background in both schemes,
       so its foreground must not follow the theme or it becomes unreadable. */
    color: #7c2d12;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    font: var(--body);
    color: var(--systemPrimary);
  }

  .field input {
    padding: 0.55rem 0.65rem;
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-xsmall);
    background: var(--pageBG);
    color: var(--systemPrimary);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font: var(--body);
  }

  .field input::placeholder {
    color: var(--systemSecondary);
  }

  .reveal {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font: var(--body);
    color: var(--systemPrimary);
  }

  button {
    padding: 0.6rem 1rem;
    border: 1px solid transparent;
    border-radius: var(--global-border-radius-xsmall);
    background: var(--keyColor);
    color: #fff;
    font: var(--body);
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  details {
    font: var(--body);
    line-height: 1.5;
    color: var(--systemPrimary);
  }

  summary {
    cursor: pointer;
    color: var(--systemPrimary);
  }

  details p {
    color: var(--systemSecondary);
  }

  pre {
    overflow-x: auto;
    padding: 0.6rem;
    border-radius: var(--global-border-radius-xsmall);
    background: #0f172a;
    color: var(--systemQuaternary);
    font: var(--callout);
  }

  .muted {
    color: var(--systemSecondary);
  }
</style>
