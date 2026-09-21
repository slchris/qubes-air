// Shared component-test setup: DOM matchers and unmounting whatever a test
// rendered, so a later test cannot find a previous test's buttons.
import '@testing-library/jest-dom/vitest'
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/svelte'

afterEach(() => cleanup())
