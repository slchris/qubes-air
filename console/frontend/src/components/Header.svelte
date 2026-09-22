<!--
  Qubes Air Console - Header Component

  The version here is the one the running server reports, never a literal. The
  header used to print its own constant ("0.1.0"), which is the same masquerade
  the build stamp exists to remove (G-H8 / M2-10): a release number no build
  ever produced, read by the operator as "this is the build I am running". The
  server stamps its own identity and serves it from /health, so the header asks
  it there, and shows nothing at all when the answer is missing — no version is
  honest, a wrong one is not.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { getHealth } from '../lib/api';

  interface Props {
    onMenuClick?: () => void;
  }

  let { onMenuClick }: Props = $props();

  // Empty means "nothing to show": the server has not answered yet, could not be
  // reached, or answered `unknown` because that binary was built without the
  // linker stamps. Only a real answer becomes a version.
  let version = $state('');

  onMount(async () => {
    try {
      version = reportedVersion((await getHealth()).version);
    } catch {
      // An unreachable console is a real state (starting up, restarting during
      // an upgrade) and it already shows in the connection indicator; claiming a
      // build here would be a guess.
      version = '';
    }
  });

  // `unknown` is what an unstamped binary reports (console/backend/internal/
  // buildinfo); it is the absence of a version, so it must not be rendered as
  // one. Anything else is passed through verbatim — the string is the server's
  // own `git describe` output, so re-spelling it (prefixing a "v", trimming the
  // `-dirty` suffix) would make the header disagree with /health.
  function reportedVersion(reported: string | undefined): string {
    if (!reported || reported === 'unknown') return '';
    return reported;
  }
</script>

<header class="header">
  <button class="menu-btn" onclick={onMenuClick} aria-label="Toggle menu">
    ☰
  </button>
  
  <div class="brand">
    <span class="logo">◇</span>
    <span class="title">Qubes Air</span>
  </div>
  
  <div class="status">
    <span class="indicator connected"></span>
    <span class="status-text">Connected</span>
  </div>
  
  <!-- No version label until the server has given one: the element's absence is
       the honest rendering of "unknown". -->
  {#if version}
    <div class="version">{version}</div>
  {/if}
</header>

<style>
  /* Nav chrome is OPAQUE in this system — blur is reserved for popovers. The
     bar was a dark slab with white text, which is a different design language;
     it now sits on the raised surface with primary ink and a hairline. */
  .header {
    display: flex;
    align-items: center;
    padding: 0 16px;
    background: var(--pageBG);
    color: var(--systemPrimary);
    border-bottom: var(--keyline-border-style);
    height: 48px;
    box-sizing: border-box;
  }

  .menu-btn {
    display: none;
    background: none;
    border: none;
    color: var(--systemSecondary);
    font: var(--title-2);
    cursor: pointer;
    padding: 4px 8px;
    margin-right: 8px;
    border-radius: var(--global-border-radius-xsmall);
  }

  @media (hover: hover) and (pointer: fine) {
    .menu-btn:hover { background: var(--systemQuinary); color: var(--systemPrimary); }
  }

  .brand {
    display: flex;
    align-items: center;
    gap: 8px;
    font: var(--title-3-emphasized);
    letter-spacing: 0;
  }

  .logo { font: var(--title-2); color: var(--keyColor); }

  .status {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 6px;
    font: var(--callout);
    color: var(--systemSecondary);
  }

  /* The markup calls this .indicator; `.connected` is what turns it green.
     A dot with no state class stays tertiary rather than claiming health. */
  .indicator {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--systemTertiary);
    flex: none;
  }
  .indicator.connected { background: var(--systemGreen); }

  .version {
    margin-left: 12px;
    font: var(--caption-1);
    color: var(--systemTertiary);
  }

  @media (max-width: 768px) {
    .menu-btn { display: block; }
  }
</style>
