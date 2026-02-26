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
  .header {
    display: flex;
    align-items: center;
    padding: 0.75rem 1rem;
    background: var(--header-bg, #2d2d2d);
    color: var(--header-text, #fff);
    border-bottom: 1px solid var(--border-color, #404040);
    height: 48px;
    box-sizing: border-box;
  }

  .menu-btn {
    display: none;
    background: none;
    border: none;
    color: inherit;
    font-size: 1.25rem;
    cursor: pointer;
    padding: 0.25rem 0.5rem;
    margin-right: 0.5rem;
    border-radius: 4px;
  }

  .menu-btn:hover {
    background: rgba(255, 255, 255, 0.1);
  }
  
  .brand {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-weight: 600;
  }
  
  .logo {
    font-size: 1.5rem;
  }
  
  .status {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.875rem;
  }
  
  .indicator {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #888;
  }
  
  .indicator.connected {
    background: #4caf50;
  }
  
  .version {
    margin-left: 1rem;
    font-size: 0.75rem;
    opacity: 0.7;
  }

  /* 响应式 - 移动端 */
  @media (max-width: 768px) {
    .menu-btn {
      display: block;
    }

    .title {
      display: none;
    }

    .status-text {
      display: none;
    }
  }
</style>
