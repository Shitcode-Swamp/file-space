import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// @testing-library/react's auto-cleanup relies on detecting a global
// `afterEach` hook; this project uses explicit imports (globals: false in
// vite.config.ts) instead of injected test globals, so it never registers
// on its own. Without this, every render() from every test in a file stays
// mounted, and a file with more than one test calling render() starts
// failing on ambiguous queries ("found multiple elements") purely because
// of leftover DOM from earlier tests, not the test's own assertions.
afterEach(() => {
  cleanup()
})
