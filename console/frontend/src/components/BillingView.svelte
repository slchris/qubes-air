<!--
  Qubes Air Console - Billing View Component
-->
<script lang="ts">
  import { getApiBaseUrl, apiFetch } from '../lib/api';

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
  // The backend flags this data as placeholder and explains why: there is no
  // cost source wired, and a bare $0.00 reads as "you owe nothing" rather than
  // "nothing is being measured". Dropping the flag is what made it read as a
  // figure.
  let placeholder = $state(false);
  let note = $state<string | null>(null);
  let error = $state<string | null>(null);

  async function loadBilling() {
    loading = true;
    error = null;
    try {
      const response = await apiFetch(`/billing`);
      if (!response.ok) throw new Error('Failed to load billing');
      const data = await response.json();
      placeholder = data.placeholder === true;
      note = data.note || null;
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
    {#if placeholder}
      <div class="placeholder-banner">
        <strong>Not a bill.</strong>
        {note || 'No cost source is connected; these figures are placeholders, not measurements.'}
      </div>
    {/if}
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
  .placeholder-banner {
    margin-bottom: 16px; padding: 10px 12px;
    border-radius: var(--global-border-radius-xsmall);
    border: 1px solid var(--systemOrange);
    background: color-mix(in srgb, var(--systemOrange) 12%, var(--pageBG));
    color: var(--systemPrimary); font: var(--body); line-height: 1.5;
  }

  .billing-view {
    max-width: 1000px;
  }

  .header {
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

  .loading, .error, .empty {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: var(--systemRed);
  }

  .summary-cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  .summary-card {
    background: var(--pageBG);
    border-radius: var(--global-border-radius-small);
    padding: 1.5rem;
    box-shadow: var(--shadow-small);
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .card-label {
    font: var(--body);
    color: var(--systemSecondary);
  }

  .card-value {
    font: var(--large-title-emphasized);
    font-weight: 600;
  }

  .card-value.muted {
    color: var(--systemSecondary);
  }

  .card-value.accent {
    color: var(--keyColor);
  }

  .section {
    margin-top: 2rem;
  }

  .table {
    width: 100%;
    border-collapse: collapse;
    background: var(--pageBG);
    border-radius: var(--global-border-radius-xsmall);
    overflow: hidden;
    box-shadow: var(--shadow-small);
  }

  .table th, .table td {
    padding: 0.75rem 1rem;
    text-align: left;
    border-bottom: 1px solid var(--systemQuaternary);
  }

  .table th {
    background: var(--systemQuinary);
    font-weight: 500;
  }
</style>
