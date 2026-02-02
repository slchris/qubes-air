<!--
  Qubes Air Console - Header Component
-->
<script lang="ts">
  const version = '0.1.0'
</script>

<header class="header">
  <div class="brand">
    <span class="logo">◇</span>
    <span class="title">Qubes Air</span>
  </div>
  
  <div class="status">
    <span class="indicator connected"></span>
    <span>Connected</span>
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
</style>
