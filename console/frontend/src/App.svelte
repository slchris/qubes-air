<!--
  Qubes Air Console - Main App Component
  
  设计原则:
  - 轻量化: 无动画、无 WebGL
  - 使用系统字体
  - 高对比度配色
-->
<script lang="ts">
  import Header from './components/Header.svelte'
  import Sidebar from './components/Sidebar.svelte'
  import ZoneList from './components/ZoneList.svelte'
  import QubeList from './components/QubeList.svelte'
  
  let currentView = $state('zones');

  function handleViewChange(view: string): void {
    currentView = view;
  }
</script>

<div class="app">
  <Header />
  
  <div class="main">
    <Sidebar {currentView} onViewChange={handleViewChange} />
    
    <main class="content">
      {#if currentView === 'zones'}
        <ZoneList />
      {:else if currentView === 'qubes'}
        <QubeList />
      {:else}
        <div class="placeholder">
          <h2>Qubes Air Console</h2>
          <p>Select a view from the sidebar</p>
        </div>
      {/if}
    </main>
  </div>
</div>

<style>
  .app {
    display: flex;
    flex-direction: column;
    height: 100vh;
    background: var(--bg-color, #f5f5f5);
    color: var(--text-color, #1a1a1a);
  }
  
  .main {
    display: flex;
    flex: 1;
    overflow: hidden;
  }
  
  .content {
    flex: 1;
    padding: 1rem;
    overflow-y: auto;
  }
  
  .placeholder {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    height: 100%;
    text-align: center;
  }
  
  /* 深色模式支持 */
  @media (prefers-color-scheme: dark) {
    .app {
      --bg-color: #1a1a1a;
      --text-color: #e0e0e0;
    }
  }
</style>
