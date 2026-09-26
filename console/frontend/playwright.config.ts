import { defineConfig, devices } from '@playwright/test';

// Browser tests of the built console against a MOCKED API.
//
// Every /api/v1 and /health request is answered by page.route() inside the
// spec, so no console backend, cluster or qube is involved. These tests prove
// the UI drives the API correctly (login gate, create, status polling,
// suspend, purge confirmation); they are not evidence that a real qube
// provisions. That is QA-01/QA-02 on real hardware.
//
// The server is `vite preview` of a production build, so what is exercised is
// the bundle that ships, not the dev server's module graph. Port 4173 is fixed
// (--strictPort) so a stale server on it fails the run instead of being tested
// by accident.
const port = 4173;
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: './e2e',
  // One browser, one worker: the specs share no state, but the suite is small
  // and a serial run keeps the CI log readable.
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  // No retries: a flaky UI test should fail the job, not pass on its second go.
  retries: 0,
  reporter: 'list',
  use: {
    baseURL,
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  webServer: {
    command: `npm run build && npm run preview -- --host 127.0.0.1 --port ${port} --strictPort`,
    url: baseURL,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
