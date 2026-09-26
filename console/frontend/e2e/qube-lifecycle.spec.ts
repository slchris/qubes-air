import { expect, test } from '@playwright/test';
import type { Page, Route } from '@playwright/test';

// The main operator path through the built console, against a mocked API.
//
// The mock answers only what the path needs and records every other request,
// before or after login, and each test asserts that list is empty: a UI change
// that starts calling an endpoint this file does not know fails here rather
// than passing on a 401 or 404 the page happened to tolerate. Nothing here talks to a real console, cluster or
// qube (see playwright.config.ts).

const TOKEN = 'test-console-token';
const QUBE_NAME = 'e2e-workstation';

interface MockQube {
  id: string;
  name: string;
  type: string;
  status: string;
  purge_requested: boolean;
  agent_health: string;
  spec: { vcpu: number; memory: number; disk: number; data_disk_gb: number };
  created_at: string;
  updated_at: string;
}

/** A job the mock runs: its output so far and whether it has ended. */
interface MockJob {
  action: string;
  state: 'running' | 'succeeded';
  log: string;
}

/** The server side of the mock: what the test flips between steps. */
interface Backend {
  authenticated: boolean;
  qube: MockQube | null;
  jobs: Record<string, MockJob>;
  /** How many times each job's live stream was opened. */
  streamOpens: Record<string, number>;
  createdPayload: unknown;
  purgeBodies: unknown[];
  launched: string[];
  unhandled: string[];
}

function newBackend(): Backend {
  return {
    authenticated: false, qube: null, jobs: {}, streamOpens: {},
    createdPayload: null, purgeBodies: [], launched: [], unhandled: [],
  };
}

// Job output is ASCII so string length equals the byte offsets the API uses.
function startJob(backend: Backend, id: string, action: string): void {
  backend.jobs[id] = { action, state: 'running', log: `${action}: started\n` };
}

function finishJob(backend: Backend, id: string): void {
  const running = backend.jobs[id];
  running.log += `${running.action}: complete\n`;
  running.state = 'succeeded';
}

function health() {
  return {
    status: 'healthy', database: 'connected',
    worker: { dispatcher: 'running', queued: 0, running: 0 },
    version: 'v0.0.0-e2e', revision: '0'.repeat(40), build_time: '2026-09-26T00:00:00Z', tree: 'clean',
    audit_trail: 'ok',
  };
}

function jobBody(id: string, running: MockJob) {
  return {
    id, qube_id: 'q-e2e', qube_name: QUBE_NAME, action: running.action, state: running.state,
    enqueued_at: '2026-09-26T00:00:00Z', started_at: '2026-09-26T00:00:01Z',
    ...(running.state === 'running' ? {} : { finished_at: '2026-09-26T00:00:30Z' }),
  };
}

// GET /jobs/:id/log?offset=N (job_handler.go Log): what exists after offset.
function logChunk(running: MockJob, offset: number) {
  const data = running.log.slice(offset);
  return { offset: offset + data.length, data, running: running.state === 'running', state: running.state };
}

// GET /jobs/:id/log/stream?offset=N, following job_handler.go LogStream:
// - the backlog after offset as one {offset,data,running:true} event;
// - once the job has ended, a terminal {offset,data:'',running:false,state};
// - while it is still running, the stream just ends. That is what the server's
//   duration cap looks like to the client, which reconnects from its offset.
function streamBody(running: MockJob, offset: number): string {
  const events: object[] = [];
  const data = running.log.slice(offset);
  const next = offset + data.length;
  if (data) events.push({ offset: next, data, running: true });
  if (running.state !== 'running') events.push({ offset: next, data: '', running: false, state: running.state });
  return events.map((e) => `data: ${JSON.stringify(e)}\n\n`).join('');
}

type Handler = (route: Route, backend: Backend, params: string[]) => Promise<void>;

interface MockRoute {
  method: string;
  pattern: RegExp;
  /** Answered without a session (only the login exchange). */
  public?: boolean;
  handle: Handler;
}

async function notFound(route: Route): Promise<void> {
  await route.fulfill({ status: 404, json: { error: 'Not Found', message: 'qube not found' } });
}

/** Runs `then` for the mock's one qube, or answers 404 as the console would. */
function forQube(then: (route: Route, backend: Backend, qube: MockQube, params: string[]) => Promise<void>): Handler {
  return async (route, backend, params) => {
    const qube = backend.qube;
    if (!qube || params[0] !== qube.id) return notFound(route);
    return then(route, backend, qube, params);
  };
}

async function serveJob(route: Route, backend: Backend, id: string, part: 'job' | 'log' | 'stream'): Promise<void> {
  const running = backend.jobs[id];
  if (!running) {
    await route.fulfill({ status: 404, json: { error: 'Job not found' } });
    return;
  }
  const offset = Number(new URL(route.request().url()).searchParams.get('offset') ?? 0) || 0;
  if (part === 'stream') {
    backend.streamOpens[id] = (backend.streamOpens[id] ?? 0) + 1;
    await route.fulfill({ contentType: 'text/event-stream', body: streamBody(running, offset) });
  } else if (part === 'log') {
    await route.fulfill({ json: logChunk(running, offset) });
  } else {
    await route.fulfill({ json: jobBody(id, running) });
  }
}

// Every (method, path) the console may call on this path. Whether a request is
// known is decided HERE, before the session check, so an unexpected call is
// recorded even when it is made (and refused) before login.
// What POST /session and GET /session report for the operator's fleet-wide
// control token: labels only, never the token (zones is always an array).
const SESSION_SCOPE = { subject: 'operator', scope: 'control', zones: [] as string[] };

const ROUTES: MockRoute[] = [
  {
    method: 'POST', pattern: /^\/session$/, public: true,
    handle: async (route, backend) => {
      const body = route.request().postDataJSON() as { token?: string };
      backend.authenticated = body.token === TOKEN;
      await route.fulfill(backend.authenticated
        ? { status: 200, json: { ...SESSION_SCOPE, expires_at: '2026-09-26T00:30:00Z' } }
        : { status: 401, json: { error: 'Unauthorized', message: 'invalid token' } });
    },
  },
  // GET /session (session_handler.go Current): the scope the session cookie
  // resolves to. The UI reads it before it renders the shell; before login the
  // session check below answers 401, as the real console does.
  { method: 'GET', pattern: /^\/session$/, handle: (route) => route.fulfill({ json: SESSION_SCOPE }) },
  {
    method: 'GET', pattern: /^\/qubes$/,
    handle: async (route, backend) => {
      const qubes = backend.qube ? [backend.qube] : [];
      await route.fulfill({ json: { qubes, total: qubes.length } });
    },
  },
  {
    method: 'POST', pattern: /^\/qubes$/,
    handle: async (route, backend) => {
      backend.createdPayload = route.request().postDataJSON();
      backend.qube = {
        id: 'q-e2e', name: QUBE_NAME, type: 'work', status: 'creating', purge_requested: false,
        agent_health: 'starting', spec: { vcpu: 2, memory: 2048, disk: 20, data_disk_gb: 20 },
        created_at: '2026-09-26T00:00:00Z', updated_at: '2026-09-26T00:00:00Z',
      };
      startJob(backend, 'job-create', 'provision');
      await route.fulfill({ status: 202, json: { qube: backend.qube, job_id: 'job-create' } });
    },
  },
  { method: 'GET', pattern: /^\/zones$/, handle: (route) => route.fulfill({ json: { zones: [], total: 0 } }) },
  { method: 'GET', pattern: /^\/jobs$/, handle: (route) => route.fulfill({ json: { jobs: [], count: 0 } }) },
  { method: 'GET', pattern: /^\/jobs\/([^/]+)$/, handle: (route, b, [id]) => serveJob(route, b, id, 'job') },
  { method: 'GET', pattern: /^\/jobs\/([^/]+)\/log$/, handle: (route, b, [id]) => serveJob(route, b, id, 'log') },
  { method: 'GET', pattern: /^\/jobs\/([^/]+)\/log\/stream$/, handle: (route, b, [id]) => serveJob(route, b, id, 'stream') },
  {
    method: 'GET', pattern: /^\/qubes\/([^/]+)$/,
    handle: forQube(async (route, _b, qube) => route.fulfill({ json: qube })),
  },
  {
    method: 'POST', pattern: /^\/qubes\/([^/]+)\/stop$/,
    handle: forQube(async (route, backend, qube) => {
      qube.status = 'suspending';
      startJob(backend, 'job-suspend', 'suspend');
      await route.fulfill({ status: 202, json: { qube, job_id: 'job-suspend' } });
    }),
  },
  {
    method: 'GET', pattern: /^\/qubes\/([^/]+)\/appmenus$/,
    handle: forQube(async (route) => route.fulfill({
      contentType: 'text/plain',
      body: ['firefox.desktop:Name=Firefox', 'firefox.desktop:Exec=qubes-desktop-run firefox.desktop'].join('\n'),
    })),
  },
  {
    method: 'POST', pattern: /^\/qubes\/([^/]+)\/apps\/([^/]+)\/launch$/,
    handle: forQube(async (route, backend, _qube, [, encodedApp]) => {
      const app = decodeURIComponent(encodedApp);
      backend.launched.push(app);
      await route.fulfill({ contentType: 'text/plain', body: `qubes.StartApp: launched '${app}' on :100\n` });
    }),
  },
  {
    method: 'POST', pattern: /^\/qubes\/([^/]+)\/purge$/,
    handle: forQube(async (route, backend, qube) => {
      backend.purgeBodies.push(route.request().postDataJSON());
      qube.status = 'deleting';
      qube.purge_requested = true;
      await route.fulfill({ status: 202, json: {} });
    }),
  },
];

async function mockConsole(page: Page, backend: Backend): Promise<void> {
  await page.route('**/health', (route) => route.fulfill({ json: health() }));
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace(/^\/api\/v1/, '');
    const method = route.request().method();
    let params: string[] = [];
    const known = ROUTES.find((r) => {
      const m = r.method === method ? r.pattern.exec(path) : null;
      if (m) params = m.slice(1);
      return m !== null;
    });
    if (!known) {
      // Recorded whether or not there is a session; answered the way the
      // console would answer it (401 before login, 404 after).
      backend.unhandled.push(`${method} ${path}`);
      await route.fulfill(backend.authenticated
        ? { status: 404, json: { error: 'Not Found', message: `unmocked ${method} ${path}` } }
        : { status: 401, json: { error: 'Unauthorized' } });
      return;
    }
    if (!known.public && !backend.authenticated) {
      await route.fulfill({ status: 401, json: { error: 'Unauthorized' } });
      return;
    }
    await known.handle(route, backend, params);
  });
}

async function unlock(page: Page, token: string): Promise<void> {
  await expect(page.getByRole('heading', { name: 'Qubes Air Console' })).toBeVisible();
  await page.getByLabel('API token').fill(token);
  await page.getByRole('button', { name: 'Unlock console' }).click();
}

/** Everything page script can read back: both web storages and document.cookie. */
async function readableState(page: Page): Promise<string> {
  return page.evaluate(() => {
    const dump = (store: Storage) =>
      Array.from({ length: store.length }, (_, i) => `${store.key(i)}=${store.getItem(store.key(i) ?? '')}`);
    return JSON.stringify([dump(localStorage), dump(sessionStorage), document.cookie]);
  });
}

function statusCell(page: Page) {
  return page.locator('.qrow .c-status');
}

test('operator logs in, provisions a qube, starts an app, suspends it, and purges it', async ({ page }) => {
  const backend = newBackend();
  await mockConsole(page, backend);

  // Purge takes a confirm and then the typed name. The first attempt cancels
  // at the name prompt; the second types it.
  const prompts: string[] = [];
  page.on('dialog', async (dialog) => {
    if (dialog.type() === 'confirm') {
      await dialog.accept();
    } else if (dialog.type() === 'prompt') {
      prompts.push(dialog.message());
      if (prompts.length === 1) await dialog.dismiss();
      else await dialog.accept(QUBE_NAME);
    } else {
      await dialog.dismiss();
    }
  });

  // The console has no session yet, so its first API call is refused and the
  // gate replaces the shell.
  await page.goto('/#qubes');
  await unlock(page, TOKEN);
  await expect(page.getByRole('heading', { name: 'Remote Qubes' })).toBeVisible();
  await expect(page.getByText('No remote qubes')).toBeVisible();

  // The token is exchanged for an HttpOnly cookie and dropped: nothing the page
  // can read may still hold it once the console is unlocked.
  expect(await readableState(page)).not.toContain(TOKEN);

  // Create. Provisioning is asynchronous: the row appears in a transient status
  // and the store polls it while the job's output streams into the row.
  await page.getByRole('button', { name: '+ Create Qube' }).click();
  const form = page.getByRole('dialog', { name: 'Create Qube' });
  await form.getByLabel('Name', { exact: true }).fill(QUBE_NAME);
  await form.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(form).toBeHidden();
  expect(backend.createdPayload).toMatchObject({ name: QUBE_NAME, type: 'work', spec: { vcpu: 2, memory: 2048 } });
  await expect(statusCell(page)).toHaveText('Provisioning…');
  await expect(page.getByRole('button', { name: 'Provisioning…' })).toBeDisabled();
  await expect(page.locator('.agent.starting')).toHaveText('starting');

  // The live job log: the running job's output streams into the row while the
  // job is still running. A panel stuck on "Waiting for … output" fails here
  // (G-H6: JobLog used to abort its own stream right after mounting).
  const logPanel = page.locator('.qrow-log');
  await expect(logPanel.getByText('provision: started')).toBeVisible({ timeout: 5_000 });

  // The job ends and the store polls the qube out of its transient status.
  finishJob(backend, 'job-create');
  backend.qube!.status = 'running';
  backend.qube!.agent_health = 'healthy';
  await expect(statusCell(page)).toHaveText('running', { timeout: 10_000 });
  // The stream reconnects from its offset, gets the rest of the output and
  // the terminal running:false event, and then stays closed.
  await expect(logPanel.getByText(/provision: started\s+provision: complete/)).toBeVisible({ timeout: 10_000 });
  const opens = backend.streamOpens['job-create'];
  await page.waitForTimeout(3_000);
  expect(backend.streamOpens['job-create']).toBe(opens);

  // Start a desktop app from the menu the qube reports.
  await page.getByRole('button', { name: 'Apps' }).click();
  const apps = page.getByRole('dialog', { name: 'Applications' });
  await apps.getByRole('button', { name: 'Launch Firefox' }).click();
  await expect(apps.getByRole('status')).toContainText('Launch request sent for Firefox');
  expect(backend.launched).toEqual(['firefox.desktop']);
  await apps.getByRole('button', { name: 'Close applications' }).click();
  await expect(apps).toBeHidden();
  await expect(page.getByRole('button', { name: 'Apps' })).toBeFocused();

  // Suspend destroys the compute and keeps the data disk.
  await page.getByRole('button', { name: 'Suspend' }).click();
  await expect(statusCell(page)).toHaveText('Suspending…');
  finishJob(backend, 'job-suspend');
  backend.qube!.status = 'suspended';
  backend.qube!.agent_health = 'unknown';
  await expect(statusCell(page)).toHaveText('Suspended (data kept)', { timeout: 10_000 });
  await expect(page.getByRole('button', { name: 'Apps' })).toHaveCount(0);

  // On a phone-width viewport the header row goes and every cell stacks, but
  // the row's controls must stay reachable.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator('.qhead')).toBeHidden();
  await expect(page.locator('.qrow .c-spec')).toHaveCSS('display', 'block');
  const purge = page.getByRole('button', { name: 'Purge', exact: true });
  await expect(purge).toBeInViewport();

  // Cancelling at the name prompt sends nothing.
  await purge.click();
  await expect.poll(() => prompts.length).toBe(1);
  expect(prompts[0]).toContain(QUBE_NAME);
  expect(backend.purgeBodies).toEqual([]);

  // Typing the name sends it as the confirmation the backend checks.
  await purge.click();
  await expect.poll(() => backend.purgeBodies.length).toBe(1);
  expect(backend.purgeBodies[0]).toEqual({ confirm: QUBE_NAME });
  await expect(statusCell(page)).toHaveText('Deleting…');
  backend.qube!.status = 'purged';
  await expect(statusCell(page)).toHaveText('Purged', { timeout: 10_000 });

  expect(backend.unhandled).toEqual([]);
});

test('a refused token keeps the console locked', async ({ page }) => {
  const backend = newBackend();
  await mockConsole(page, backend);

  await page.goto('/#qubes');
  await unlock(page, 'not-the-token');

  await expect(page.getByText('The API token was rejected')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Unlock console' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Remote Qubes' })).toHaveCount(0);
  expect(await readableState(page)).not.toContain('not-the-token');
  expect(backend.unhandled).toEqual([]);
});
