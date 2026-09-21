import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Dedicated test config: the app's vite.config.ts builds a dev-server setup
// (proxy, TLS, env logging) that tests do not need, and reusing it would make
// every test run print the proxy banner. The svelte plugin is still required
// because api.ts imports auth.svelte.ts, whose runes must be compiled.
export default defineConfig({
  plugins: [svelte()],
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
