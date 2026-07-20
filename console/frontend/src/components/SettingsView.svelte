<!--
  Qubes Air Console - Settings View Component
-->
<script lang="ts">
  import { getApiBaseUrl, apiFetch, getApiToken, setApiToken } from '../lib/api';

  interface Settings {
    general: {
      timezone: string;
      language: string;
      theme: 'light' | 'dark' | 'system';
    };
    notifications: {
      email: boolean;
      webhook: boolean;
      webhookUrl: string;
    };
    security: {
      sessionTimeout: number;
      twoFactorEnabled: boolean;
    };
  }

  let settings = $state<Settings>({
    general: {
      timezone: 'UTC',
      language: 'en',
      theme: 'system',
    },
    notifications: {
      email: true,
      webhook: false,
      webhookUrl: '',
    },
    security: {
      sessionTimeout: 30,
      twoFactorEnabled: false,
    },
  });

  let loading = $state(true);
  let saving = $state(false);
  let error = $state<string | null>(null);
  let success = $state<string | null>(null);

  async function loadSettings() {
    loading = true;
    error = null;
    try {
      const response = await apiFetch(`/settings`);
      if (!response.ok) throw new Error('Failed to load settings');
      const data = await response.json();
      if (data.settings) {
        // 逐个更新设置以保持响应性
        settings.general.timezone = data.settings.general?.timezone || 'UTC';
        settings.general.language = data.settings.general?.language || 'en';
        settings.general.theme = data.settings.general?.theme || 'system';
        settings.notifications.email = data.settings.notifications?.email ?? true;
        settings.notifications.webhook = data.settings.notifications?.webhook ?? false;
        settings.notifications.webhookUrl = data.settings.notifications?.webhookUrl || '';
        settings.security.sessionTimeout = data.settings.security?.sessionTimeout || 30;
        settings.security.twoFactorEnabled = data.settings.security?.twoFactorEnabled ?? false;
      }
    } catch (e) {
      error = e instanceof Error ? e.message : 'Unknown error';
    } finally {
      loading = false;
    }
  }

  let apiToken = $state('');
  let hasStoredToken = $state(Boolean(getApiToken()));
  let tokenMessage = $state('');

  /**
   * Persists the API token in this browser.
   *
   * Deliberately not round-tripped through the server: it is the credential
   * used to talk to the server, so storing it there would be circular, and
   * echoing it back would widen its exposure.
   */
  function saveToken(): void {
    setApiToken(apiToken);
    hasStoredToken = Boolean(apiToken);
    tokenMessage = apiToken ? 'Token saved for this browser' : 'Token cleared';
    apiToken = '';
    setTimeout(() => { tokenMessage = ''; }, 4000);
  }

  async function saveSettings() {
    saving = true;
    error = null;
    success = null;
    try {
      const response = await apiFetch(`/settings`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(settings),
      });
      if (!response.ok) throw new Error('Failed to save settings');
      success = 'Settings saved successfully';
      setTimeout(() => success = null, 3000);
    } catch (e) {
      error = e instanceof Error ? e.message : 'Unknown error';
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    loadSettings();
  });

  const timezones = ['UTC', 'America/New_York', 'America/Los_Angeles', 'Europe/London', 'Europe/Paris', 'Asia/Tokyo', 'Asia/Shanghai'];
  const languages = [
    { value: 'en', label: 'English' },
    { value: 'zh', label: '中文' },
  ];
</script>

<div class="settings-view">
  <div class="header">
    <h2>Settings</h2>
  </div>

  {#if loading}
    <div class="loading">Loading settings...</div>
  {:else}
    {#if error}
      <div class="message error">{error}</div>
    {/if}
    {#if success}
      <div class="message success">{success}</div>
    {/if}

    <!-- API token lives outside the settings form: it is a browser-local
         credential used to authenticate requests, not a server-side setting.
         Saving it through the settings endpoint would require the very
         authentication it provides. -->
    <section class="section">
      <h3>API authentication</h3>
      <div class="field">
        <label for="api-token">API token</label>
        <input
          id="api-token"
          type="password"
          bind:value={apiToken}
          placeholder={hasStoredToken ? '•••••••• (stored)' : 'Bearer token from auth.api_token'}
          autocomplete="off"
        />
        <small class="hint">
          Sent as <code>Authorization: Bearer …</code> on every API request.
          Required once the server has <code>auth.api_token</code> set — without
          it every request returns 401. Stored in this browser only; it is never
          sent to the settings endpoint.
        </small>
      </div>
      <div class="field">
        <button type="button" class="btn-primary" onclick={saveToken}>
          {apiToken ? 'Save token' : 'Clear token'}
        </button>
        {#if tokenMessage}<span class="token-message">{tokenMessage}</span>{/if}
      </div>
    </section>

    <form onsubmit={(e) => { e.preventDefault(); saveSettings(); }}>
      <section class="section">
        <h3>General</h3>
        
        <div class="field">
          <label for="timezone">Timezone</label>
          <select id="timezone" bind:value={settings.general.timezone}>
            {#each timezones as tz}
              <option value={tz}>{tz}</option>
            {/each}
          </select>
        </div>

        <div class="field">
          <label for="language">Language</label>
          <select id="language" bind:value={settings.general.language}>
            {#each languages as lang}
              <option value={lang.value}>{lang.label}</option>
            {/each}
          </select>
        </div>

        <div class="field">
          <label for="theme">Theme</label>
          <select id="theme" bind:value={settings.general.theme}>
            <option value="system">System</option>
            <option value="light">Light</option>
            <option value="dark">Dark</option>
          </select>
        </div>
      </section>

      <section class="section">
        <h3>Notifications</h3>
        
        <div class="field checkbox">
          <input type="checkbox" id="email-notify" bind:checked={settings.notifications.email} />
          <label for="email-notify">Email Notifications</label>
        </div>

        <div class="field checkbox">
          <input type="checkbox" id="webhook-notify" bind:checked={settings.notifications.webhook} />
          <label for="webhook-notify">Webhook Notifications</label>
        </div>

        {#if settings.notifications.webhook}
          <div class="field">
            <label for="webhook-url">Webhook URL</label>
            <input type="url" id="webhook-url" bind:value={settings.notifications.webhookUrl} placeholder="https://..." />
          </div>
        {/if}
      </section>

      <section class="section">
        <h3>Security</h3>
        
        <div class="field">
          <label for="session-timeout">Session Timeout (minutes)</label>
          <input type="number" id="session-timeout" bind:value={settings.security.sessionTimeout} min="5" max="1440" />
        </div>

        <div class="field checkbox">
          <input type="checkbox" id="two-factor" bind:checked={settings.security.twoFactorEnabled} />
          <label for="two-factor">Two-Factor Authentication</label>
        </div>
      </section>

      <div class="actions">
        <button type="submit" class="btn-primary" disabled={saving}>
          {saving ? 'Saving...' : 'Save Settings'}
        </button>
      </div>
    </form>
  {/if}
</div>

<style>
  .settings-view {
    max-width: 600px;
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
    border-bottom: 1px solid var(--systemQuaternary);
    padding-bottom: 0.5rem;
  }

  .loading {
    padding: 2rem;
    text-align: center;
  }

  .message {
    padding: 0.75rem 1rem;
    border-radius: var(--global-border-radius-xsmall);
    margin-bottom: 1rem;
  }

  .message.error {
    background: color-mix(in srgb, var(--systemRed) 10%, var(--pageBG));
    color: var(--systemRed);
  }

  .message.success {
    background: color-mix(in srgb, var(--systemGreen) 10%, var(--pageBG));
    color: var(--systemGreen);
  }

  .section {
    background: var(--pageBG);
    border-radius: var(--global-border-radius-small);
    padding: 1.5rem;
    margin-bottom: 1.5rem;
    box-shadow: var(--shadow-small);
  }

  .field {
    margin-bottom: 1rem;
  }

  .field:last-child {
    margin-bottom: 0;
  }

  .field label {
    display: block;
    font-weight: 500;
    margin-bottom: 0.375rem;
  }

  .field.checkbox {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .field.checkbox label {
    margin: 0;
    font-weight: normal;
  }

  .field input[type="text"],
  .field input[type="url"],
  .field input[type="number"],
  .field select {
    width: 100%;
    padding: 0.5rem;
    border: 1px solid var(--systemQuaternary);
    border-radius: var(--global-border-radius-xsmall);
    font: var(--title-3);
  }

  .field input:focus,
  .field select:focus {
    outline: none;
    border-color: var(--keyColor);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
  }

  .btn-primary {
    padding: 0.625rem 1.5rem;
    background: var(--keyColor);
    color: white;
    border: none;
    border-radius: var(--global-border-radius-xsmall);
    cursor: pointer;
    font: var(--title-2);
  }

  .btn-primary:hover:not(:disabled) {
    background: var(--keyColor-pressed);
  }

  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  /* 响应式布局 */
  @media (max-width: 768px) {
    .settings-view {
      max-width: 100%;
    }

    .section {
      padding: 1rem;
      margin-bottom: 1rem;
    }

    h2 {
      font: var(--title-1-emphasized);
    }

    h3 {
      font: var(--title-2);
    }

    .actions {
      justify-content: stretch;
    }

    .btn-primary {
      width: 100%;
    }
  }

  .hint {
    display: block;
    margin-top: 0.35rem;
    font: var(--subhead);
    color: var(--text-muted, var(--systemSecondary));
    line-height: 1.5;
  }
  .hint code {
    font-size: 0.72rem;
    padding: 0.05rem 0.25rem;
    border-radius: 3px;
    background: var(--bg-subtle, rgba(128, 128, 128, 0.15));
  }
  .token-message {
    margin-left: 0.75rem;
    font: var(--callout);
    color: var(--success, var(--systemGreen));
  }
</style>
