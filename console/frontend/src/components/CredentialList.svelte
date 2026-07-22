<!--
  Qubes Air Console - Credentials List Component
-->
<script lang="ts">
  import { apiFetch } from '../lib/api';

  interface Credential {
    id: string;
    name: string;
    type: 'proxmox' | 'aws' | 'gcp' | 'azure' | 'ssh' | 'api_key' | 'other';
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
    // Proxmox first: it is the provider this deployment actually uses, and its
    // absence was why a PVE credential could not be created from the UI at all.
    { value: 'proxmox', label: 'Proxmox' },
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
      const response = await apiFetch(`/credentials`);
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
      // A path relative to the API base. apiFetch prepends the base itself, so
      // passing an absolute /api/v1/... here produced /api/v1/api/v1/... and a
      // 404 on every create and edit.
      const url = editingId
        ? `/credentials/${editingId}`
        : `/credentials`;
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
      
      const response = await apiFetch(url, {
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
      const response = await apiFetch(`/credentials/${id}`, {
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
  <div class="modal-layer">
    <button type="button" class="modal-backdrop" aria-label="Close credential dialog" onclick={closeModal}></button>
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="credential-dialog-title" tabindex="-1">
      <div class="modal-header">
        <h3 id="credential-dialog-title">{editingId ? 'Edit Credential' : 'Add Credential'}</h3>
        <button type="button" class="btn-close" aria-label="Close credential dialog" onclick={closeModal}>×</button>
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
    font: var(--title-1-emphasized);
  }

  .btn-primary {
    padding: 0.5rem 1rem;
    background: var(--keyColor);
    color: white;
    border: none;
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
  }

  .btn-primary:hover {
    background: var(--keyColor-pressed);
  }

  .loading, .error, .empty {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: var(--systemRed);
  }

  .hint {
    color: var(--systemSecondary);
    font: var(--body);
  }

  .card-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 1rem;
  }

  .card {
    background: var(--pageBG);
    border-radius: var(--global-border-radius-small);
    padding: 1rem;
    box-shadow: var(--shadow-small);
  }

  .card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }

  /* The one uppercase role in this system is the eyebrow: 600 11px, secondary
     ink, no tracking. */
  .card-type {
    font: var(--subhead-emphasized);
    color: var(--systemSecondary);
    padding: 2px 6px;
    background: var(--systemQuinary);
    border-radius: var(--global-border-radius-xsmall);
    text-transform: uppercase;
    letter-spacing: 0;
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
    background: var(--systemQuinary);
  }

  .btn-danger:hover {
    background: color-mix(in srgb, var(--systemRed) 10%, var(--pageBG));
    color: var(--systemRed);
  }

  .card-name {
    margin: 0 0 0.5rem;
    font: var(--title-2-emphasized);
  }

  .card-desc {
    margin: 0 0 0.75rem;
    font: var(--body);
    color: var(--systemSecondary);
  }

  .card-meta {
    font: var(--subhead);
    color: var(--systemSecondary);
  }

  /* Modal styles */
  .modal-layer {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .modal-backdrop {
    position: absolute;
    inset: 0;
    padding: 0;
    border: 0;
    background: rgba(0, 0, 0, 0.5);
    cursor: default;
  }

  .modal {
    position: relative;
    z-index: 1;
    background: var(--pageBG);
    border-radius: var(--global-border-radius-small);
    width: 100%;
    max-width: 480px;
    box-shadow: var(--shadow-medium);
  }

  .modal-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 1rem 1.5rem;
    border-bottom: 1px solid var(--systemQuaternary);
  }

  .modal-header h3 {
    margin: 0;
    font: var(--title-1-emphasized);
  }

  .btn-close {
    background: none;
    border: none;
    font: var(--title-1-emphasized);
    cursor: pointer;
    color: var(--systemSecondary);
  }

  .btn-secondary {
    padding: 0.5rem 1rem;
    background: var(--systemQuinary);
    color: var(--systemPrimary);
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
  }

  .btn-secondary:hover {
    background: var(--systemQuaternary);
  }

  .modal-form {
    padding: 1.5rem;
  }

  .form-error {
    background: color-mix(in srgb, var(--systemRed) 10%, var(--pageBG));
    color: var(--systemRed);
    padding: 0.75rem;
    border-radius: var(--global-border-radius-xsmall);
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
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-xsmall);
    font: var(--title-2);
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
