<!--
  Qubes Air Console - Monitoring View Component
-->
<script lang="ts">
  import { apiFetch, getZoneCapacity, listZones } from '../lib/api';
  import type { Zone, ZoneCapacity } from '../lib/types';
  import { SCHEDULER_HEADROOM } from '../lib/types';

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

  // Real fleet data. /monitoring describes the console's own Go runtime; the
  // numbers that say anything about the infrastructure come per-zone from
  // /zones/:id/capacity, which was already implemented and only ever consumed
  // inside the create-qube modal.
  interface ZoneCap { zone: Zone; cap: ZoneCapacity | null; error: string | null }
  let zoneCaps = $state<ZoneCap[]>([]);
  let loadingCaps = $state(false);

  async function loadCapacity() {
    loadingCaps = true;
    try {
      const zr = await listZones();
      const zones = zr.zones ?? [];
      zoneCaps = await Promise.all(zones.map(async (z) => {
        try {
          return { zone: z, cap: await getZoneCapacity(z.id), error: null };
        } catch (e) {
          // 503 (unreachable / no credential) and 501 (no scheduler) are
          // expected states, not faults — a disconnected zone simply has no
          // capacity to report.
          // "Service Unavailable" is the HTTP reason phrase, not an
          // explanation. A zone reports no capacity for a small number of
          // reasons and each is actionable, so name the reason.
          const raw = e instanceof Error ? e.message : '';
          const reason = z.status !== 'connected'
            ? 'not connected'
            : /501|not implemented/i.test(raw)
              ? 'provider reports no capacity'
              : 'cluster unreachable — check the endpoint and credential';
          return { zone: z, cap: null, error: reason };
        }
      }));
    } catch {
      zoneCaps = [];
    } finally {
      loadingCaps = false;
    }
  }

  function gib(bytes: number): string {
    return (bytes / 1024 ** 3).toFixed(1);
  }
  function memPct(n: { mem_used_bytes: number; mem_total_bytes: number }): number {
    return n.mem_total_bytes ? (n.mem_used_bytes / n.mem_total_bytes) * 100 : 0;
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
    loadCapacity();
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

    <!-- Real numbers first. Everything below this section describes the console
         process, not the fleet, and leading with it was how "Disk 0%" came to
         be read as a measurement. -->
    <section class="capacity">
      <h3>Cluster capacity</h3>
      {#if loadingCaps}
        <p class="muted">Loading…</p>
      {:else if zoneCaps.length === 0}
        <p class="muted">No zones configured.</p>
      {:else}
        {#each zoneCaps as zc (zc.zone.id)}
          <div class="zone-cap">
            <div class="zc-head">
              <span class="zc-name">{zc.zone.name}</span>
              <span class="zc-type">{zc.zone.type}</span>
              {#if zc.error}
                <span class="zc-err">{zc.error}</span>
              {/if}
            </div>

            {#if zc.cap?.nodes?.length}
              <table class="nodes">
                <thead>
                  <tr>
                    <th>Node</th><th>CPU</th><th>Memory</th><th>Free</th>
                  </tr>
                </thead>
                <tbody>
                  {#each zc.cap.nodes as n (n.name)}
                    <tr class:offline={!n.online}>
                      <td>{n.name}{#if !n.online} <span class="muted">offline</span>{/if}</td>
                      <td>{(n.cpu_usage * 100).toFixed(1)}% <span class="muted">of {n.max_cpu}c</span></td>
                      <td>
                        <span class="bar"><span class="fill" style="width:{memPct(n).toFixed(1)}%"></span></span>
                        {memPct(n).toFixed(1)}%
                      </td>
                      <td>{gib(n.mem_free_bytes)} GiB</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
              <p class="muted small">
                The scheduler keeps {(SCHEDULER_HEADROOM * 100).toFixed(0)}% of a node's memory
                unused, so a node with less than that free will not take another qube.
              </p>
            {:else if zc.cap?.quota}
              <p class="muted">
                Elastic provider — usage against quota rather than a node pool.
              </p>
            {:else if !zc.error}
              <p class="muted">No capacity reported.</p>
            {/if}
          </div>
        {/each}
      {/if}
    </section>

    {#if placeholder}
      <div class="placeholder-banner">
        <strong>Placeholder metrics.</strong>
        {note || 'These describe the console process, not the managed qubes or zones.'}
        Per-node cluster capacity is real — see the Zones view.
      </div>
    {/if}

    <h3 class="ph-head">Console process</h3>
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
  .capacity { margin-bottom: 32px; }
  .capacity h3, .ph-head { margin: 0 0 12px; font: var(--title-2-emphasized); }
  .ph-head { margin-top: 8px; }
  .muted { color: var(--systemSecondary); font: var(--body); margin: 0; }
  .muted.small { font: var(--callout); margin-top: 8px; }

  .zone-cap {
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-small);
    background: var(--pageBG);
    padding: 12px 16px;
    margin-bottom: 12px;
  }
  .zc-head { display: flex; align-items: baseline; gap: 8px; margin-bottom: 8px; }
  .zc-name { font: var(--title-3-emphasized); }
  .zc-type { font: var(--subhead); color: var(--systemSecondary); }
  .zc-err { font: var(--callout); color: var(--systemOrange); margin-left: auto; }

  .nodes { width: 100%; border-collapse: collapse; font: var(--body); }
  .nodes th {
    text-align: left; font: var(--subhead-emphasized); color: var(--systemSecondary);
    text-transform: uppercase; letter-spacing: 0;
    padding: 4px 8px 6px 0; border-bottom: 1px solid var(--systemQuaternary);
  }
  .nodes td { padding: 6px 8px 6px 0; border-bottom: 1px solid var(--systemQuinary); }
  .nodes tr.offline td { color: var(--systemTertiary); }

  .bar {
    display: inline-block; width: 90px; height: 6px; vertical-align: middle;
    background: var(--systemQuinary); border-radius: 3px; overflow: hidden; margin-right: 6px;
  }
  .bar .fill { display: block; height: 100%; background: var(--keyColor); }

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
