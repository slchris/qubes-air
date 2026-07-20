<!--
  Qubes Air Console - Header Component
-->
<script lang="ts">
  interface Props {
    onMenuClick?: () => void;
  }

  let { onMenuClick }: Props = $props();
  
  const version = '0.1.0'
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
  
  <div class="version">v{version}</div>
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
