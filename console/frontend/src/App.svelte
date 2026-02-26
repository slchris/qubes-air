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
  import QubeList from './components/QubeList.svelte'
  import InfraList from './components/InfraList.svelte'
  import CredentialList from './components/CredentialList.svelte'
  import BillingView from './components/BillingView.svelte'
  import MonitoringView from './components/MonitoringView.svelte'
  import SettingsView from './components/SettingsView.svelte'
  
  // 从 URL hash 获取当前视图，支持页面刷新保持状态
  function getViewFromHash(): string {
    const hash = window.location.hash.slice(1); // 移除 #
    const validViews = ['qubes', 'infrastructure', 'credentials', 'billing', 'monitoring', 'settings'];
    return validViews.includes(hash) ? hash : 'qubes';
  }

  let currentView = $state(getViewFromHash());
  let sidebarOpen = $state(false);

  // 监听 hash 变化
  $effect(() => {
    const handleHashChange = () => {
      currentView = getViewFromHash();
    };
    window.addEventListener('hashchange', handleHashChange);
    return () => window.removeEventListener('hashchange', handleHashChange);
  });

  function handleViewChange(view: string): void {
    currentView = view;
    window.location.hash = view;
    sidebarOpen = false; // 移动端选择后关闭侧边栏
  }

  function toggleSidebar(): void {
    sidebarOpen = !sidebarOpen;
  }
</script>

<div class="app">
  <Header onMenuClick={toggleSidebar} />
  
  <div class="main">
    <!-- 移动端遮罩层 -->
    {#if sidebarOpen}
      <div class="sidebar-overlay" onclick={() => sidebarOpen = false}></div>
    {/if}
    
    <Sidebar {currentView} onViewChange={handleViewChange} isOpen={sidebarOpen} />
    
    <main class="content">
      {#if currentView === 'qubes'}
        <QubeList />
      {:else if currentView === 'infrastructure'}
        <InfraList />
      {:else if currentView === 'credentials'}
        <CredentialList />
      {:else if currentView === 'billing'}
        <BillingView />
      {:else if currentView === 'monitoring'}
        <MonitoringView />
      {:else if currentView === 'settings'}
        <SettingsView />
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
    position: relative;
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

  /* 移动端遮罩层 */
  .sidebar-overlay {
    display: none;
  }
  
  /* 深色模式支持 */
  @media (prefers-color-scheme: dark) {
    .app {
      --bg-color: #1a1a1a;
      --text-color: #e0e0e0;
    }
  }

  /* 响应式布局 - 平板 */
  @media (max-width: 1024px) {
    .content {
      padding: 0.75rem;
    }
  }

  /* 响应式布局 - 移动端 */
  @media (max-width: 768px) {
    .sidebar-overlay {
      display: block;
      position: fixed;
      top: 0;
      left: 0;
      right: 0;
      bottom: 0;
      background: rgba(0, 0, 0, 0.5);
      z-index: 99;
    }

    .content {
      padding: 0.5rem;
    }
  }
</style>
