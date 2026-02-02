<!--
  Qubes Air Console - Sidebar Component
-->
<script lang="ts">
  interface Props {
    currentView: string;
    onViewChange: (view: string) => void;
  }

  let { currentView, onViewChange }: Props = $props();
  
  const menuItems = [
    { id: 'zones', label: 'Zones', icon: '◈' },
    { id: 'qubes', label: 'Qubes', icon: '□' },
    { id: 'settings', label: 'Settings', icon: '⚙' },
  ]
</script>

<aside class="sidebar">
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
    background: var(--sidebar-bg, #f0f0f0);
    border-right: 1px solid var(--border-color, #ddd);
  }
  
  .nav {
    display: flex;
    flex-direction: column;
    padding: 0.5rem;
  }
  
  .nav-item {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    background: none;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    text-align: left;
    font-size: 0.9375rem;
    color: inherit;
  }
  
  .nav-item:hover {
    background: var(--hover-bg, rgba(0,0,0,0.05));
  }
  
  .nav-item.active {
    background: var(--active-bg, rgba(0,0,0,0.1));
    font-weight: 500;
  }
  
  .icon {
    font-size: 1.125rem;
  }
  
  @media (prefers-color-scheme: dark) {
    .sidebar {
      --sidebar-bg: #252525;
      --border-color: #333;
    }
    
    .nav-item:hover {
      --hover-bg: rgba(255,255,255,0.05);
    }
    
    .nav-item.active {
      --active-bg: rgba(255,255,255,0.1);
    }
  }
</style>
