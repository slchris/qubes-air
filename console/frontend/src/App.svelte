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
  import CredentialList from './components/CredentialList.svelte'
  import BillingView from './components/BillingView.svelte'
  import MonitoringView from './components/MonitoringView.svelte'
  import SettingsView from './components/SettingsView.svelte'
  import Dashboard from './components/Dashboard.svelte'
  import ZonesView from './components/ZonesView.svelte'
  import JobsView from './components/JobsView.svelte'
  import LoginGate from './components/LoginGate.svelte'
  import { auth } from './lib/auth.svelte'
  
  // 从 URL hash 获取当前视图，支持页面刷新保持状态
  function getViewFromHash(): string {
    const hash = window.location.hash.slice(1); // 移除 #
    const validViews = ['dashboard', 'qubes', 'zones', 'jobs', 'credentials', 'billing', 'monitoring', 'settings'];
    // Dashboard is the landing view: opening straight onto the qube list answers
    // "what exists" but not "is anything wrong", and the two facts that matter
    // most on arrival — an unreachable agent and a failed job — were the ones
    // that took the most clicks to find.
    return validViews.includes(hash) ? hash : 'dashboard';
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

{#if auth.required}
  <!--
    The gate replaces the whole shell rather than sitting inside it. Rendering
    the sidebar and views behind it would mount every view, fire every request,
    and produce a screen of failures next to the one control that fixes them.
  -->
  <LoginGate />
{:else}
<div class="app">
  <Header onMenuClick={toggleSidebar} />

  <div class="main">
    <!-- 移动端遮罩层 -->
    {#if sidebarOpen}
      <div class="sidebar-overlay" onclick={() => sidebarOpen = false}></div>
    {/if}
    
    <Sidebar {currentView} onViewChange={handleViewChange} isOpen={sidebarOpen} />
    
    <main class="content">
      {#if currentView === 'dashboard'}
        <Dashboard onViewChange={handleViewChange} />
      {:else if currentView === 'qubes'}
        <QubeList />
      {:else if currentView === 'jobs'}
        <JobsView />
      {:else if currentView === 'zones'}
        <ZonesView />
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
{/if}

<style>
  /* Colours, type and radii come from styles/tokens.css. Nothing here declares
     a hex value or a font size of its own. */
  .app {
    display: flex;
    flex-direction: column;
    /* dvh, not vh: on mobile browsers vh counts the area behind the collapsing
       address bar, so a 100vh shell is taller than what is actually visible and
       the bottom of the page cannot be reached. */
    height: 100dvh;
    background: var(--pageBg);
    color: var(--systemPrimary);
  }

  .main {
    display: flex;
    flex: 1;
    overflow: hidden;
    position: relative;
    /* A flex item's default min-height is auto, i.e. "never shrink below your
       content". Without this the row refuses to shrink, .content's overflow-y
       never engages, and the excess is clipped by the overflow: hidden above
       with no way to scroll to it — which is exactly what zooming in produced,
       since zoom shrinks the viewport while the content keeps its size. */
    min-height: 0;
  }

  .content {
    flex: 1;
    padding: var(--bodyGutter);
    overflow-y: auto;
  }

  /* Content is capped and CENTRED here, once, rather than each view pinning its
     own max-width. Views were setting 1100px and inheriting the default
     left alignment, so on a wide display everything crowded into a narrow
     left-hand column with the rest of the screen empty. */
  .content > :global(*) {
    max-width: 1680px;
    margin-inline: auto;
  }

  .placeholder {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    height: 100%;
    text-align: center;
  }

  .sidebar-overlay { display: none; }

  @media (max-width: 768px) {
    .sidebar-overlay {
      display: block;
      position: fixed;
      inset: 0;
      background: var(--modalScrimColor);
      z-index: 99;
    }
    .content { padding: 16px; }
  }
</style>
