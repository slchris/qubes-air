<!--
  Qubes Air Console - Sidebar Component
-->
<script lang="ts">
  interface Props {
    currentView: string;
    onViewChange: (view: string) => void;
    isOpen?: boolean;
  }

  let { currentView, onViewChange, isOpen = false }: Props = $props();
  
  const menuItems = [
    { id: 'dashboard', label: 'Dashboard', icon: '◎' },
    { id: 'qubes', label: 'Qubes', icon: '□' },
    { id: 'zones', label: 'Zones', icon: '◈' },
    { id: 'jobs', label: 'Jobs', icon: '≡' },
    { id: 'credentials', label: 'Credentials', icon: '⚿' },
    { id: 'billing', label: 'Billing', icon: '$' },
    { id: 'monitoring', label: 'Monitoring', icon: '◉' },
    { id: 'settings', label: 'Settings', icon: '⚙' },
  ]
</script>

<aside class="sidebar" class:open={isOpen}>
  <nav class="nav">
    {#each menuItems as item}
      <button
        class="nav-item"
        class:active={currentView === item.id}
        onclick={() => onViewChange(item.id)}
      >
        <span class="icon">{item.icon}</span>
        <span class="label">{item.label}</span>
      </button>
    {/each}
  </nav>
</aside>

<style>
  .sidebar {
    width: 200px;
    /* The system's own nav-sidebar token: a 3% ink wash over the page floor,
       not a grey hex. */
    background: var(--navSidebarBG);
    border-right: var(--keyline-border-style);
    flex-shrink: 0;
    /* The parent row clips its overflow, so without a scroller of its own the
       lower nav items become unreachable once the viewport is shorter than the
       menu — which browser zoom causes directly. */
    overflow-y: auto;
  }

  .nav {
    display: flex;
    flex-direction: column;
    padding: 8px;
    gap: 2px;
  }

  .nav-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    background: none;
    border: none;
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
    text-align: left;
    font: var(--title-3);
    letter-spacing: 0;
    color: var(--systemSecondary);
    transition: var(--hover-transition);
  }

  /* Hover is gated: a touch device has no hover state to give, and leaving it
     ungated leaves the last-tapped item stuck looking active. */
  @media (hover: hover) and (pointer: fine) {
    .nav-item:hover {
      background: var(--systemQuinary);
      color: var(--systemPrimary);
    }
  }

  /* Selection is carried by weight and ink, not a heavy fill. */
  .nav-item.active {
    background: var(--systemQuinary);
    font: var(--title-3-emphasized);
    color: var(--systemPrimary);
  }

  .icon {
    font: var(--title-3);
    width: 1.25em;
    text-align: center;
    color: var(--systemSecondary);
  }
  .nav-item.active .icon { color: var(--keyColor); }

  @media (max-width: 768px) {
    .sidebar {
      position: fixed;
      top: 48px;
      bottom: 0;
      left: 0;
      z-index: 100;
      transform: translateX(-100%);
      transition: transform var(--hover-transition);
      background: var(--pageBG);
    }
    .sidebar.open { transform: translateX(0); }
  }

  @media (prefers-reduced-motion: reduce) {
    .sidebar, .nav-item { transition: none; }
  }
</style>
