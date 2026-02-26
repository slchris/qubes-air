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
    { id: 'qubes', label: 'Qubes', icon: '□' },
    { id: 'infrastructure', label: 'Infrastructure', icon: '◈' },
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
    background: var(--sidebar-bg, #f0f0f0);
    border-right: 1px solid var(--border-color, #ddd);
    flex-shrink: 0;
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

  /* 响应式 - 移动端 */
  @media (max-width: 768px) {
    .sidebar {
      position: fixed;
      top: 48px; /* Header 高度 */
      left: 0;
      bottom: 0;
      z-index: 100;
      transform: translateX(-100%);
      transition: transform 0.2s ease;
      box-shadow: 2px 0 8px rgba(0, 0, 0, 0.1);
    }

    .sidebar.open {
      transform: translateX(0);
    }
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
