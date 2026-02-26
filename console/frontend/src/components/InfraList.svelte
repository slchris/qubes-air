<!--
  Qubes Air Console - Infrastructure List Component
-->
<script lang="ts">
  import { getApiBaseUrl } from '../lib/api';

  interface InfraProvider {
    id: string;
    name: string;
    type: string;
    status: 'connected' | 'disconnected' | 'error';
    region: string;
    resourceCount: number;
    createdAt: string;
  }

  interface FormData {
    name: string;
    type: string;
    region: string;
  }

  let providers = $state<InfraProvider[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let showModal = $state(false);
  let editingId = $state<string | null>(null);
  let formData = $state<FormData>({ name: '', type: 'aws', region: 'us-east-1' });
  let formError = $state<string | null>(null);

  const providerTypes = ['aws', 'gcp', 'azure', 'kubernetes', 'docker', 'other'];
  const regions = ['us-east-1', 'us-west-2', 'eu-west-1', 'eu-central-1', 'ap-southeast-1', 'ap-northeast-1'];

  async function loadProviders() {
    loading = true;
    error = null;
    try {
      const response = await fetch(`${getApiBaseUrl()}/infrastructure`);
      if (!response.ok) throw new Error('Failed to load infrastructure');
      const data = await response.json();
      providers = data.providers || [];
    } catch (e) {
      error = e instanceof Error ? e.message : 'Unknown error';
      providers = [];
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    loadProviders();
  });

  function openAddModal() {
    editingId = null;
    formData = { name: '', type: 'aws', region: 'us-east-1' };
    formError = null;
    showModal = true;
  }

  function openEditModal(provider: InfraProvider) {
    editingId = provider.id;
    formData = { name: provider.name, type: provider.type, region: provider.region };
    formError = null;
    showModal = true;
  }

  function closeModal() {
    showModal = false;
    editingId = null;
    formError = null;
  }

  async function handleSubmit() {
    formError = null;
    if (!formData.name.trim()) {
      formError = 'Name is required';
      return;
    }

    try {
      const url = editingId 
        ? `${getApiBaseUrl()}/infrastructure/${editingId}`
        : `${getApiBaseUrl()}/infrastructure`;
      const method = editingId ? 'PUT' : 'POST';
      
      const response = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(formData)
      });

      if (!response.ok) {
        const data = await response.json();
        throw new Error(data.error || 'Failed to save');
      }

      closeModal();
      loadProviders();
    } catch (e) {
      formError = e instanceof Error ? e.message : 'Unknown error';
    }
  }

  async function handleDelete(id: string, name: string) {
    if (!confirm(`Are you sure you want to delete "${name}"?`)) return;

    try {
      const response = await fetch(`${getApiBaseUrl()}/infrastructure/${id}`, {
        method: 'DELETE'
      });

      if (!response.ok) throw new Error('Failed to delete');
      loadProviders();
    } catch (e) {
      alert(e instanceof Error ? e.message : 'Failed to delete');
    }
  }

  async function handleConnect(id: string) {
    try {
      const response = await fetch(`${getApiBaseUrl()}/infrastructure/${id}/connect`, {
        method: 'POST'
      });
      if (!response.ok) throw new Error('Failed to connect');
      loadProviders();
    } catch (e) {
      alert(e instanceof Error ? e.message : 'Connection failed');
    }
  }

  async function handleDisconnect(id: string) {
    try {
      const response = await fetch(`${getApiBaseUrl()}/infrastructure/${id}/disconnect`, {
        method: 'POST'
      });
      if (!response.ok) throw new Error('Failed to disconnect');
      loadProviders();
    } catch (e) {
      alert(e instanceof Error ? e.message : 'Disconnection failed');
    }
  }

  function getStatusIcon(status: string): string {
    switch (status) {
      case 'connected': return '●';
      case 'disconnected': return '○';
      case 'error': return '✕';
      default: return '?';
    }
  }

  function getStatusClass(status: string): string {
    switch (status) {
      case 'connected': return 'status-connected';
      case 'disconnected': return 'status-disconnected';
      case 'error': return 'status-error';
      default: return '';
    }
  }
</script>

<div class="infra-list">
  <div class="header">
    <h2>Infrastructure</h2>
    <button class="btn-primary" onclick={openAddModal}>
      + Add Provider
    </button>
  </div>

  {#if loading}
    <div class="loading">Loading infrastructure...</div>
  {:else if error}
    <div class="error">
      <p>Error: {error}</p>
      <button onclick={loadProviders}>Retry</button>
    </div>
  {:else if providers.length === 0}
    <div class="empty">
      <p>No infrastructure providers configured.</p>
      <p class="hint">Add a cloud provider to get started.</p>
    </div>
  {:else}
    <table class="table">
      <thead>
        <tr>
          <th>Name</th>
          <th>Type</th>
          <th>Region</th>
          <th>Resources</th>
          <th>Status</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        {#each providers as provider}
          <tr>
            <td class="name">{provider.name}</td>
            <td>{provider.type.toUpperCase()}</td>
            <td>{provider.region}</td>
            <td>{provider.resourceCount}</td>
            <td class={getStatusClass(provider.status)}>
              <span class="status-icon">{getStatusIcon(provider.status)}</span>
              {provider.status}
            </td>
            <td class="actions">
              {#if provider.status === 'disconnected'}
                <button class="btn-small btn-connect" onclick={() => handleConnect(provider.id)}>Connect</button>
              {:else if provider.status === 'connected'}
                <button class="btn-small btn-disconnect" onclick={() => handleDisconnect(provider.id)}>Disconnect</button>
              {/if}
              <button class="btn-small" onclick={() => openEditModal(provider)}>Edit</button>
              <button class="btn-small btn-danger" onclick={() => handleDelete(provider.id, provider.name)}>Delete</button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

{#if showModal}
  <div class="modal-overlay" onclick={closeModal}>
    <div class="modal" onclick={(e) => e.stopPropagation()}>
      <div class="modal-header">
        <h3>{editingId ? 'Edit Provider' : 'Add Provider'}</h3>
        <button class="btn-close" onclick={closeModal}>×</button>
      </div>
      <form class="modal-form" onsubmit={(e) => { e.preventDefault(); handleSubmit(); }}>
        {#if formError}
          <div class="form-error">{formError}</div>
        {/if}
        <div class="form-group">
          <label for="name">Name</label>
          <input type="text" id="name" bind:value={formData.name} placeholder="My AWS Account" />
        </div>
        <div class="form-group">
          <label for="type">Type</label>
          <select id="type" bind:value={formData.type}>
            {#each providerTypes as t}
              <option value={t}>{t.toUpperCase()}</option>
            {/each}
          </select>
        </div>
        <div class="form-group">
          <label for="region">Region</label>
          <select id="region" bind:value={formData.region}>
            {#each regions as r}
              <option value={r}>{r}</option>
            {/each}
          </select>
        </div>
        <div class="modal-actions">
          <button type="button" class="btn-secondary" onclick={closeModal}>Cancel</button>
          <button type="submit" class="btn-primary">{editingId ? 'Save' : 'Create'}</button>
        </div>
      </form>
    </div>
  </div>
{/if}

<style>
  .infra-list {
    max-width: 1200px;
  }

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 1rem;
  }

  h2 {
    margin: 0;
    font-size: 1.5rem;
  }

  .btn-primary {
    padding: 0.5rem 1rem;
    background: #0066cc;
    color: white;
    border: none;
    border-radius: 4px;
    cursor: pointer;
  }

  .btn-primary:hover {
    background: #0052a3;
  }

  .btn-secondary {
    padding: 0.5rem 1rem;
    background: #f0f0f0;
    color: #333;
    border: 1px solid #ddd;
    border-radius: 4px;
    cursor: pointer;
  }

  .btn-secondary:hover {
    background: #e0e0e0;
  }

  .loading, .error, .empty {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: #cc0000;
  }

  .hint {
    color: #666;
    font-size: 0.875rem;
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

  .name {
    font-weight: 500;
  }

  .status-connected { color: #00aa00; }
  .status-disconnected { color: #666; }
  .status-error { color: #cc0000; }

  .status-icon {
    margin-right: 0.25rem;
  }

  .actions {
    white-space: nowrap;
  }

  .btn-small {
    padding: 0.25rem 0.5rem;
    font-size: 0.75rem;
    background: #f0f0f0;
    border: 1px solid #ddd;
    border-radius: 3px;
    cursor: pointer;
    margin-right: 0.25rem;
  }

  .btn-small:hover {
    background: #e0e0e0;
  }

  .btn-danger {
    color: #cc0000;
  }

  .btn-danger:hover {
    background: #ffeeee;
  }

  .btn-connect {
    color: #00aa00;
  }

  .btn-connect:hover {
    background: #eeffee;
  }

  .btn-disconnect {
    color: #ff8800;
  }

  .btn-disconnect:hover {
    background: #fff8ee;
  }

  /* Modal styles */
  .modal-overlay {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .modal {
    background: white;
    border-radius: 8px;
    width: 100%;
    max-width: 480px;
    box-shadow: 0 4px 20px rgba(0, 0, 0, 0.2);
  }

  .modal-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 1rem 1.5rem;
    border-bottom: 1px solid #eee;
  }

  .modal-header h3 {
    margin: 0;
    font-size: 1.25rem;
  }

  .btn-close {
    background: none;
    border: none;
    font-size: 1.5rem;
    cursor: pointer;
    color: #666;
  }

  .modal-form {
    padding: 1.5rem;
  }

  .form-error {
    background: #ffeeee;
    color: #cc0000;
    padding: 0.75rem;
    border-radius: 4px;
    margin-bottom: 1rem;
  }

  .form-group {
    margin-bottom: 1rem;
  }

  .form-group label {
    display: block;
    margin-bottom: 0.25rem;
    font-weight: 500;
  }

  .form-group input,
  .form-group select {
    width: 100%;
    padding: 0.5rem;
    border: 1px solid #ddd;
    border-radius: 4px;
    font-size: 1rem;
    box-sizing: border-box;
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 1.5rem;
  }

  @media (prefers-color-scheme: dark) {
    .table {
      background: #2a2a2a;
    }

    .table th {
      background: #333;
    }

    .table th, .table td {
      border-bottom-color: #444;
    }

    .btn-small {
      background: #333;
      border-color: #444;
      color: #e0e0e0;
    }

    .btn-small:hover {
      background: #444;
    }

    .hint {
      color: #999;
    }

    .modal {
      background: #2a2a2a;
    }

    .modal-header {
      border-bottom-color: #444;
    }

    .btn-close {
      color: #999;
    }

    .form-group input,
    .form-group select {
      background: #333;
      border-color: #444;
      color: #e0e0e0;
    }

    .btn-secondary {
      background: #333;
      border-color: #444;
      color: #e0e0e0;
    }

    .btn-secondary:hover {
      background: #444;
    }
  }

  /* 响应式布局 */
  @media (max-width: 1024px) {
    .table th, .table td {
      padding: 0.5rem 0.75rem;
    }
  }

  @media (max-width: 768px) {
    .header {
      flex-direction: column;
      align-items: flex-start;
      gap: 0.75rem;
    }

    .btn-primary {
      width: 100%;
    }

    /* 移动端使用卡片布局替代表格 */
    .table {
      display: none;
    }

    .infra-list {
      max-width: 100%;
    }

    .modal {
      margin: 0.5rem;
      max-width: calc(100% - 1rem);
    }
  }
</style>
