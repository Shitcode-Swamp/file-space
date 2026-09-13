import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    // Explicit imports (`import { describe, it, expect, vi } from 'vitest'`)
    // are used throughout instead of relying on injected globals, so that
    // test files type-check cleanly under tsconfig.app.json without adding
    // "vitest/globals" to its `types` array.
    globals: false,
    setupFiles: ['./src/setupTests.ts'],
  },
})
