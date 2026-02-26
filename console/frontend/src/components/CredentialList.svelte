<!--
  Qubes Air Console - Credentials List Component
-->
<script lang="ts">
  import { getApiBaseUrl } from '../lib/api';

  interface Credential {
    id: string;
    name: string;
    type: 'aws' | 'gcp' | 'azure' | 'ssh' | 'api_key' | 'other';
    description: string;
    lastUsed: string | null;
    createdAt: string;
  }

  interface FormData {
    name: string;
    type: string;
    description: string;
    secret: string;
  }

  let credentials = $state<Credential[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let showModal = $state(false);
  let editingId = $state<string | null>(null);
  let formData = $state<FormData>({ name: '', type: 'aws', description: '', secret: '' });
  let formError = $state<string | null>(null);

  const credentialTypes = [
    { value: 'aws', label: 'AWS' },
    { value: 'gcp', label: 'Google Cloud' },
    { value: 'azure', label: 'Azure' },
    { value: 'ssh', label: 'SSH Key' },
    { value: 'api_key', label: 'API Key' },
    { value: 'other', label: 'Other' }
  ];

  async function loadCredentials() {
    loading = true;
    error = null;
    try {
      const response = await fetch(`${getApiBaseUrl()}/credentials`);
      if (!response.ok) throw new Error('Failed to load credentials');
      const data = await response.json();
      credentials = data.credentials || [];
    } catch (e) {
      error = e instanceof Error ? e.message : 'Unknown error';
      credentials = [];
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    loadCredentials();
  });

  function openAddModal() {
    editingId = null;
    formData = { name: '', type: 'aws', description: '', secret: '' };
    formError = null;
    showModal = true;
  }

  function openEditModal(cred: Credential) {
    editingId = cred.id;
    formData = { name: cred.name, type: cred.type, description: cred.description, secret: '' };
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
    if (!editingId && !formData.secret.trim()) {
      formError = 'Secret is required';
      return;
    }

    try {
      const url = editingId 
        ? `${getApiBaseUrl()}/credentials/${editingId}`
        : `${getApiBaseUrl()}/credentials`;
      const method = editingId ? 'PUT' : 'POST';
      
      const body: any = {
        name: formData.name,
        description: formData.description
      };
      
      if (!editingId) {
        body.type = formData.type;
        body.secret = formData.secret;
      } else if (formData.secret) {
        body.secret = formData.secret;
      }
      
      const response = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
      });

      if (!response.ok) {
        const data = await response.json();
        throw new Error(data.error || 'Failed to save');
      }

      closeModal();
      loadCredentials();
    } catch (e) {
      formError = e instanceof Error ? e.message : 'Unknown error';
    }
  }

  async function handleDelete(id: string, name: string) {
    if (!confirm(`Are you sure you want to delete "${name}"? This cannot be undone.`)) return;

    try {
      const response = await fetch(`${getApiBaseUrl()}/credentials/${id}`, {
        method: 'DELETE'
      });

      if (!response.ok) throw new Error('Failed to delete');
      loadCredentials();
    } catch (e) {
      alert(e instanceof Error ? e.message : 'Failed to delete');
    }
  }

  function getTypeLabel(type: string): string {
    const found = credentialTypes.find(t => t.value === type);
    return found ? found.label : type;
  }

  function formatDate(dateStr: string | null): string {
    if (!dateStr) return 'Never';
    return new Date(dateStr).toLocaleDateString();
  }
</script>

<div class="credential-list">
  <div class="header">
    <h2>Credentials</h2>
    <button class="btn-primary" onclick={openAddModal}>
      + Add Credential
    </button>
  </div>

  {#if loading}
    <div class="loading">Loading credentials...</div>
  {:else if error}
    <div class="error">
      <p>Error: {error}</p>
      <button onclick={loadCredentials}>Retry</button>
    </div>
  {:else if credentials.length === 0}
    <div class="empty">
      <p>No credentials configured.</p>
      <p class="hint">Add credentials to connect to cloud providers.</p>
    </div>
  {:else}
    <div class="card-grid">
      {#each credentials as cred}
        <div class="card">
          <div class="card-header">
            <span class="card-type">{getTypeLabel(cred.type)}</span>
            <span class="card-actions">
              <button class="btn-icon" title="Edit" onclick={() => openEditModal(cred)}>✎</button>
              <button class="btn-icon btn-danger" title="Delete" onclick={() => handleDelete(cred.id, cred.name)}>✕</button>
            </span>
          </div>
          <h3 class="card-name">{cred.name}</h3>
          {#if cred.description}
            <p class="card-desc">{cred.description}</p>
          {/if}
          <div class="card-meta">
            <span>Last used: {formatDate(cred.lastUsed)}</span>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

{#if showModal}
  <div class="modal-overlay" onclick={closeModal}>
    <div class="modal" onclick={(e) => e.stopPropagation()}>
      <div class="modal-header">
        <h3>{editingId ? 'Edit Credential' : 'Add Credential'}</h3>
        <button class="btn-close" onclick={closeModal}>×</button>
      </div>
      <form class="modal-form" onsubmit={(e) => { e.preventDefault(); handleSubmit(); }}>
        {#if formError}
          <div class="form-error">{formError}</div>
        {/if}
        <div class="form-group">
          <label for="name">Name</label>
          <input type="text" id="name" bind:value={formData.name} placeholder="Production AWS" />
        </div>
        {#if !editingId}
          <div class="form-group">
            <label for="type">Type</label>
            <select id="type" bind:value={formData.type}>
              {#each credentialTypes as t}
                <option value={t.value}>{t.label}</option>
              {/each}
            </select>
          </div>
        {/if}
        <div class="form-group">
          <label for="description">Description</label>
          <input type="text" id="description" bind:value={formData.description} placeholder="Optional description" />
        </div>
        <div class="form-group">
          <label for="secret">
            Secret {editingId ? '(leave empty to keep existing)' : ''}
          </label>
          <textarea id="secret" bind:value={formData.secret} rows="4" placeholder="Enter API key, access token, or credentials..."></textarea>
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
  .credential-list {
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

  .card-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 1rem;
  }

  .card {
    background: white;
    border-radius: 8px;
    padding: 1rem;
    box-shadow: 0 1px 3px rgba(0,0,0,0.1);
  }

  .card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }

  .card-type {
    font-size: 0.75rem;
    padding: 0.125rem 0.5rem;
    background: #e0e0e0;
    border-radius: 3px;
    text-transform: uppercase;
  }

  .card-actions {
    display: flex;
    gap: 0.25rem;
  }

  .btn-icon {
    width: 24px;
    height: 24px;
    padding: 0;
    background: transparent;
    border: none;
    cursor: pointer;
    border-radius: 3px;
  }

  .btn-icon:hover {
    background: #f0f0f0;
  }

  .btn-danger:hover {
    background: #ffeeee;
    color: #cc0000;
  }

  .card-name {
    margin: 0 0 0.5rem;
    font-size: 1.125rem;
  }

  .card-desc {
    margin: 0 0 0.75rem;
    font-size: 0.875rem;
    color: #666;
  }

  .card-meta {
    font-size: 0.75rem;
    color: #888;
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
  .form-group select,
  .form-group textarea {
    width: 100%;
    padding: 0.5rem;
    border: 1px solid #ddd;
    border-radius: 4px;
    font-size: 1rem;
    box-sizing: border-box;
    font-family: inherit;
  }

  .form-group textarea {
    resize: vertical;
    min-height: 80px;
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 1.5rem;
  }

  @media (prefers-color-scheme: dark) {
    .card {
      background: #2a2a2a;
    }

    .card-type {
      background: #444;
    }

    .btn-icon:hover {
      background: #444;
    }

    .card-desc {
      color: #999;
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
    .form-group select,
    .form-group textarea {
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
  @media (max-width: 768px) {
    .header {
      flex-direction: column;
      align-items: flex-start;
      gap: 0.75rem;
    }

    .btn-primary {
      width: 100%;
    }

    .card-grid {
      grid-template-columns: 1fr;
    }

    .credential-list {
      max-width: 100%;
    }

    .modal {
      margin: 0.5rem;
      max-width: calc(100% - 1rem);
    }

    .modal-form {
      padding: 1rem;
    }
  }
</style>
