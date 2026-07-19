<!--
  Qubes Air Console - Monitoring View Component
-->
<script lang="ts">
  import { apiFetch } from '../lib/api';

  interface MetricPoint {
    timestamp: string;
    value: number;
  }

  interface SystemMetrics {
    cpuUsage: number;
    memoryUsage: number;
    diskUsage: number;
    networkIn: number;
    networkOut: number;
  }

  interface Alert {
    id: string;
    severity: 'critical' | 'warning' | 'info';
    message: string;
    source: string;
    timestamp: string;
    acknowledged: boolean;
  }

  let metrics = $state<SystemMetrics | null>(null);
  let alerts = $state<Alert[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  // The backend flags these numbers as placeholder and says what they actually
  // describe. Dropping that was how "Disk Usage 0%" got shown as a measurement.
  let placeholder = $state(false);
  let note = $state<string | null>(null);

  // A percentage to two places. The raw value is a float like 34.7182931, which
  // is what the operator meant by "小数点后太多了". Clamped so a bad number
  // cannot render a bar wider than its track.
  function pct(v: number | undefined): string {
    return (Math.max(0, Math.min(100, v ?? 0))).toFixed(2);
  }

  async function loadMonitoring() {
    loading = true;
    error = null;
    try {
      const response = await apiFetch(`/monitoring`);
      if (!response.ok) throw new Error('Failed to load monitoring data');
      const data = await response.json();
      metrics = data.metrics || null;
      alerts = data.alerts || [];
      placeholder = data.placeholder === true;
      note = data.note || null;
    } catch (e) {
      error = e instanceof Error ? e.message : 'Unknown error';
      metrics = null;
      alerts = [];
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    loadMonitoring();
  });

  function getSeverityClass(severity: string): string {
    return `severity-${severity}`;
  }

  function formatBytes(bytes: number): string {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
  }

  function formatTime(dateStr: string): string {
    return new Date(dateStr).toLocaleString();
  }
</script>

<div class="monitoring-view">
  <div class="header">
    <h2>Monitoring</h2>
    <button class="btn-secondary" onclick={loadMonitoring}>↻ Refresh</button>
  </div>

  {#if loading}
    <div class="loading">Loading monitoring data...</div>
  {:else if error}
    <div class="error">
      <p>Error: {error}</p>
      <button onclick={loadMonitoring}>Retry</button>
    </div>
  {:else}
    {#if placeholder}
      <div class="placeholder-banner">
        <strong>Placeholder metrics.</strong>
        {note || 'These describe the console process, not the managed qubes or zones.'}
        Per-node cluster capacity is real — see the Zones view.
      </div>
    {/if}

    <div class="metrics-grid">
      <div class="metric-card">
        <span class="metric-label">CPU Usage</span>
        <div class="metric-bar">
          <div class="metric-fill" style="width: {pct(metrics?.cpuUsage)}%"></div>
        </div>
        <span class="metric-value">{pct(metrics?.cpuUsage)}%</span>
      </div>

      <div class="metric-card">
        <span class="metric-label">Memory Usage</span>
        <div class="metric-bar">
          <div class="metric-fill" style="width: {pct(metrics?.memoryUsage)}%"></div>
        </div>
        <span class="metric-value">{pct(metrics?.memoryUsage)}%</span>
      </div>

      <div class="metric-card">
        <span class="metric-label">Disk Usage</span>
        <div class="metric-bar">
          <div class="metric-fill" style="width: {pct(metrics?.diskUsage)}%"></div>
        </div>
        <span class="metric-value">{pct(metrics?.diskUsage)}%</span>
      </div>

      <div class="metric-card">
        <span class="metric-label">Network I/O</span>
        <div class="network-stats">
          <span>↓ {formatBytes(metrics?.networkIn ?? 0)}/s</span>
          <span>↑ {formatBytes(metrics?.networkOut ?? 0)}/s</span>
        </div>
      </div>
    </div>

    <div class="section">
      <h3>Active Alerts</h3>
      {#if alerts.length === 0}
        <div class="empty-alerts">
          <span class="check-icon">✓</span>
          <p>No active alerts. All systems operational.</p>
        </div>
      {:else}
        <div class="alert-list">
          {#each alerts as alert}
            <div class="alert-item {getSeverityClass(alert.severity)}">
              <div class="alert-header">
                <span class="alert-severity">{alert.severity.toUpperCase()}</span>
                <span class="alert-time">{formatTime(alert.timestamp)}</span>
              </div>
              <p class="alert-message">{alert.message}</p>
              <span class="alert-source">Source: {alert.source}</span>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .placeholder-banner {
    margin-bottom: 1rem; padding: 0.6rem 0.8rem; border-radius: 4px;
    border: 1px solid #d97706; background: #fffbeb; color: #7c2d12;
    font-size: 0.85rem; line-height: 1.5;
  }

  .monitoring-view {
    max-width: 1200px;
  }

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 1.5rem;
  }

  h2 {
    margin: 0;
    font-size: 1.5rem;
  }

  h3 {
    margin: 0 0 1rem;
    font-size: 1.125rem;
  }

  .btn-secondary {
    padding: 0.5rem 1rem;
    background: #f0f0f0;
    border: 1px solid #ddd;
    border-radius: 4px;
    cursor: pointer;
  }

  .btn-secondary:hover {
    background: #e0e0e0;
  }

  .loading, .error {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: #cc0000;
  }

  .metrics-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  .metric-card {
    background: white;
    border-radius: 8px;
    padding: 1rem;
    box-shadow: 0 1px 3px rgba(0,0,0,0.1);
  }

  .metric-label {
    display: block;
    font-size: 0.875rem;
    color: #666;
    margin-bottom: 0.75rem;
  }

  .metric-bar {
    height: 8px;
    background: #e0e0e0;
    border-radius: 4px;
    overflow: hidden;
    margin-bottom: 0.5rem;
  }

  .metric-fill {
    height: 100%;
    background: #0066cc;
    border-radius: 4px;
    transition: width 0.3s;
  }

  .metric-value {
    font-size: 1.25rem;
    font-weight: 600;
  }

  .network-stats {
    display: flex;
    justify-content: space-between;
    font-size: 1rem;
    font-weight: 500;
  }

  .section {
    margin-top: 2rem;
  }

  .empty-alerts {
    background: #e8f5e9;
    border-radius: 8px;
    padding: 2rem;
    text-align: center;
  }

  .check-icon {
    font-size: 2rem;
    color: #4caf50;
  }

  .empty-alerts p {
    margin: 0.5rem 0 0;
    color: #2e7d32;
  }

  .alert-list {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .alert-item {
    background: white;
    border-radius: 4px;
    padding: 1rem;
    border-left: 4px solid;
    box-shadow: 0 1px 3px rgba(0,0,0,0.1);
  }

  .alert-item.severity-critical {
    border-left-color: #dc3545;
  }

  .alert-item.severity-warning {
    border-left-color: #ffc107;
  }

  .alert-item.severity-info {
    border-left-color: #17a2b8;
  }

  .alert-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }

  .alert-severity {
    font-size: 0.75rem;
    font-weight: 600;
    padding: 0.125rem 0.5rem;
    border-radius: 3px;
  }

  .severity-critical .alert-severity {
    background: #f8d7da;
    color: #721c24;
  }

  .severity-warning .alert-severity {
    background: #fff3cd;
    color: #856404;
  }

  .severity-info .alert-severity {
    background: #d1ecf1;
    color: #0c5460;
  }

  .alert-time {
    font-size: 0.75rem;
    color: #666;
  }

  .alert-message {
    margin: 0 0 0.5rem;
    font-weight: 500;
  }

  .alert-source {
    font-size: 0.75rem;
    color: #888;
  }

  @media (prefers-color-scheme: dark) {
    .btn-secondary {
      background: #333;
      border-color: #444;
      color: #e0e0e0;
    }

    .btn-secondary:hover {
      background: #444;
    }

    .metric-card {
      background: #2a2a2a;
    }

    .metric-label {
      color: #999;
    }

    .metric-bar {
      background: #444;
    }

    .empty-alerts {
      background: #1b3d1f;
    }

    .empty-alerts p {
      color: #a5d6a7;
    }

    .alert-item {
      background: #2a2a2a;
    }

    .alert-time, .alert-source {
      color: #999;
    }
  }
</style>
