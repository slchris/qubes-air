<!--
  Qubes Air Console - Billing View Component
-->
<script lang="ts">
  import { getApiBaseUrl } from '../lib/api';

  interface BillingSummary {
    currentMonth: number;
    lastMonth: number;
    projectedMonth: number;
    currency: string;
  }

  interface UsageItem {
    service: string;
    usage: number;
    unit: string;
    cost: number;
  }

  let summary = $state<BillingSummary | null>(null);
  let usage = $state<UsageItem[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  async function loadBilling() {
    loading = true;
    error = null;
    try {
      const response = await fetch(`${getApiBaseUrl()}/billing`);
      if (!response.ok) throw new Error('Failed to load billing');
      const data = await response.json();
      summary = data.summary || null;
      usage = data.usage || [];
    } catch (e) {
      error = e instanceof Error ? e.message : 'Unknown error';
      summary = null;
      usage = [];
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    loadBilling();
  });

  function formatCurrency(amount: number, currency: string = 'USD'): string {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency: currency,
    }).format(amount);
  }
</script>

<div class="billing-view">
  <div class="header">
    <h2>Billing</h2>
  </div>

  {#if loading}
    <div class="loading">Loading billing information...</div>
  {:else if error}
    <div class="error">
      <p>Error: {error}</p>
      <button onclick={loadBilling}>Retry</button>
    </div>
  {:else}
    <div class="summary-cards">
      <div class="summary-card">
        <span class="card-label">Current Month</span>
        <span class="card-value">{summary ? formatCurrency(summary.currentMonth, summary.currency) : '$0.00'}</span>
      </div>
      <div class="summary-card">
        <span class="card-label">Last Month</span>
        <span class="card-value muted">{summary ? formatCurrency(summary.lastMonth, summary.currency) : '$0.00'}</span>
      </div>
      <div class="summary-card">
        <span class="card-label">Projected</span>
        <span class="card-value accent">{summary ? formatCurrency(summary.projectedMonth, summary.currency) : '$0.00'}</span>
      </div>
    </div>

    <div class="section">
      <h3>Usage Breakdown</h3>
      {#if usage.length === 0}
        <div class="empty">No usage data available.</div>
      {:else}
        <table class="table">
          <thead>
            <tr>
              <th>Service</th>
              <th>Usage</th>
              <th>Cost</th>
            </tr>
          </thead>
          <tbody>
            {#each usage as item}
              <tr>
                <td>{item.service}</td>
                <td>{item.usage} {item.unit}</td>
                <td>{formatCurrency(item.cost, summary?.currency)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  {/if}
</div>

<style>
  .billing-view {
    max-width: 1000px;
  }

  .header {
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

  .loading, .error, .empty {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: #cc0000;
  }

  .summary-cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  .summary-card {
    background: white;
    border-radius: 8px;
    padding: 1.5rem;
    box-shadow: 0 1px 3px rgba(0,0,0,0.1);
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .card-label {
    font-size: 0.875rem;
    color: #666;
  }

  .card-value {
    font-size: 1.75rem;
    font-weight: 600;
  }

  .card-value.muted {
    color: #666;
  }

  .card-value.accent {
    color: #0066cc;
  }

  .section {
    margin-top: 2rem;
  }

  .table {
    width: 100%;
    border-collapse: collapse;
    background: white;
    border-radius: 4px;
    overflow: hidden;
    box-shadow: 0 1px 3px rgba(0,0,0,0.1);
  }

  .table th, .table td {
    padding: 0.75rem 1rem;
    text-align: left;
    border-bottom: 1px solid #eee;
  }

  .table th {
    background: #f8f8f8;
    font-weight: 500;
  }

  @media (prefers-color-scheme: dark) {
    .summary-card {
      background: #2a2a2a;
    }

    .card-label {
      color: #999;
    }

    .card-value.muted {
      color: #999;
    }

    .table {
      background: #2a2a2a;
    }

    .table th {
      background: #333;
    }

    .table th, .table td {
      border-bottom-color: #444;
    }
  }
</style>
