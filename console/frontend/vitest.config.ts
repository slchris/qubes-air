import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Dedicated test config: the app's vite.config.ts builds a dev-server setup
// (proxy, TLS, env logging) that tests do not need, and reusing it would make
// every test run print the proxy banner. The svelte plugin is still required
// because api.ts imports auth.svelte.ts, whose runes must be compiled, and
// because component tests mount .svelte files directly.
export default defineConfig({
  plugins: [svelte()],
  resolve: {
    // Components must resolve through the browser build; the server build
    // skips the DOM-specific paths and mounts nothing.
    conditions: ['browser'],
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    setupFiles: ['./src/test-setup.ts'],
  },
})
