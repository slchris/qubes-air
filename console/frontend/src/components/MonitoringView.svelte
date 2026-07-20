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
    margin-bottom: 1rem; padding: 0.6rem 0.8rem; border-radius: var(--global-border-radius-xsmall);
    border: 1px solid var(--systemOrange); background: color-mix(in srgb, var(--systemOrange) 12%, var(--pageBG)); color: var(--systemRed);
    font: var(--body); line-height: 1.5;
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
    font: var(--title-1-emphasized);
  }

  h3 {
    margin: 0 0 1rem;
    font: var(--title-2-emphasized);
  }

  .btn-secondary {
    padding: 6px 12px;
    background: var(--systemQuinary);
    /* Foreground stated next to the background. Without it the inherited colour
       landed on the fill and the control was invisible. */
    color: var(--systemPrimary);
    font: var(--body);
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
  }

  .btn-secondary:hover {
    background: var(--systemQuaternary);
  }

  .loading, .error {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: var(--systemRed);
  }

  .metrics-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  .metric-card {
    background: var(--pageBG);
    border-radius: var(--global-border-radius-small);
    padding: 1rem;
    box-shadow: var(--shadow-small);
  }

  .metric-label {
    display: block;
    font: var(--body);
    color: var(--systemSecondary);
    margin-bottom: 0.75rem;
  }

  .metric-bar {
    height: 8px;
    background: var(--systemQuaternary);
    border-radius: var(--global-border-radius-xsmall);
    overflow: hidden;
    margin-bottom: 0.5rem;
  }

  .metric-fill {
    height: 100%;
    background: var(--keyColor);
    border-radius: var(--global-border-radius-xsmall);
    transition: var(--hover-transition);
  }

  .metric-value {
    font: var(--title-1-emphasized);
    font-weight: 600;
  }

  .network-stats {
    display: flex;
    justify-content: space-between;
    font: var(--title-2);
    font-weight: 500;
  }

  .section {
    margin-top: 2rem;
  }

  .empty-alerts {
    background: color-mix(in srgb, var(--systemGreen) 10%, var(--pageBG));
    border-radius: var(--global-border-radius-small);
    padding: 2rem;
    text-align: center;
  }

  .check-icon {
    font: var(--header-emphasized);
    color: var(--systemGreen);
  }

  /* Green text on a green wash measured 1.96:1. The colour belongs to the
     check mark, which carries the meaning; the sentence reads in ink. */
  .empty-alerts p {
    margin: 8px 0 0;
    color: var(--systemSecondary);
    font: var(--body);
  }

  .alert-list {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .alert-item {
    background: var(--pageBG);
    border-radius: var(--global-border-radius-xsmall);
    padding: 1rem;
    border-left: 4px solid;
    box-shadow: var(--shadow-small);
  }

  .alert-item.severity-critical {
    border-left-color: var(--systemRed);
  }

  .alert-item.severity-warning {
    border-left-color: var(--systemOrange);
  }

  .alert-item.severity-info {
    border-left-color: var(--systemTeal);
  }

  .alert-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }

  .alert-severity {
    font: var(--subhead);
    font-weight: 600;
    padding: 0.125rem 0.5rem;
    border-radius: 3px;
  }

  .severity-critical .alert-severity {
    background: color-mix(in srgb, var(--systemRed) 10%, var(--pageBG));
    color: var(--systemRed);
  }

  .severity-warning .alert-severity {
    background: color-mix(in srgb, var(--systemOrange) 12%, var(--pageBG));
    color: var(--systemOrange);
  }

  .severity-info .alert-severity {
    background: color-mix(in srgb, var(--systemTeal) 12%, var(--pageBG));
    color: var(--systemTeal);
  }

  .alert-time {
    font: var(--subhead);
    color: var(--systemSecondary);
  }

  .alert-message {
    margin: 0 0 0.5rem;
    font-weight: 500;
  }

  .alert-source {
    font: var(--subhead);
    color: var(--systemSecondary);
  }
</style>
