import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApiFileRecord, ListFilesParams } from './api/types'
import { ACCESS_TOKEN_KEY, REFRESH_TOKEN_KEY, USERNAME_KEY } from './api/client'

// Only listFiles is mocked (App's data source); every other export
// (localStorage key constants, clearTokens, setUnauthorizedListener, ...)
// stays real so AuthProvider hydration behaves exactly as in production.
vi.mock('./api/client', async () => {
  const actual = await vi.importActual<typeof import('./api/client')>('./api/client')
  return {
    ...actual,
    listFiles: vi.fn(),
  }
})

import * as api from './api/client'
import { AuthProvider } from './auth/AuthContext'
import App from './App'

const mockedListFiles = vi.mocked(api.listFiles)

const ALL_FILES: ApiFileRecord[] = [
  {
    id: 1,
    name: 'notes.txt',
    extension: 'txt',
    size: 10,
    createdAt: '2024-01-01',
    modifiedAt: '2024-01-01',
    uploadedBy: 'alice',
    editedBy: 'alice',
  },
  {
    id: 2,
    name: 'photo.png',
    extension: 'png',
    size: 20,
    createdAt: '2024-01-02',
    modifiedAt: '2024-01-02',
    uploadedBy: 'alice',
    editedBy: 'alice',
  },
  {
    id: 3,
    name: 'Main.java',
    extension: 'java',
    size: 30,
    createdAt: '2024-01-03',
    modifiedAt: '2024-01-03',
    uploadedBy: 'bob',
    editedBy: 'bob',
  },
]

/**
 * Mimics the real backend: GET /api/files?extension=... returns only the
 * matching subset, while an unfiltered call returns everything. This is the
 * behavior that makes App.tsx's separate "all extensions" query necessary in
 * the first place (see App.tsx's `allFilesQuery` comment).
 */
function fakeListFiles(params: ListFilesParams = {}): Promise<ApiFileRecord[]> {
  if (params.extension) {
    return Promise.resolve(ALL_FILES.filter((f) => f.extension === params.extension))
  }
  return Promise.resolve(ALL_FILES)
}

function renderApp() {
  const queryClient = new QueryClient()
  return render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <App />
      </AuthProvider>
    </QueryClientProvider>,
  )
}

function optionValues(select: HTMLElement): string[] {
  return within(select)
    .getAllByRole('option')
    .map((o) => (o as HTMLOptionElement).value)
}

describe('App extension filter dropdown', () => {
  beforeEach(() => {
    localStorage.clear()
    // Hydrate a logged-in user (see AuthContext.test.tsx) so App renders the
    // drive view instead of the login page, without a real network call.
    localStorage.setItem(ACCESS_TOKEN_KEY, 'access-token')
    localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-token')
    localStorage.setItem(USERNAME_KEY, 'alice')

    mockedListFiles.mockReset()
    mockedListFiles.mockImplementation(fakeListFiles)
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('derives its options from the full file list, so selecting one extension does not make the others disappear', async () => {
    renderApp()

    const select = await screen.findByLabelText('Filter:')

    // All three extensions are present up front.
    await waitFor(() => {
      expect(optionValues(select)).toEqual(expect.arrayContaining(['txt', 'png', 'java']))
    })
    const optionsBeforeFilter = optionValues(select)

    // Select one extension — the *table data* query is now filtered...
    fireEvent.change(select, { target: { value: 'txt' } })

    await waitFor(() => {
      expect(mockedListFiles).toHaveBeenCalledWith(
        expect.objectContaining({ extension: 'txt' }),
      )
    })

    // ...but the dropdown's own option list must be unchanged: still every
    // extension, not collapsed down to just "txt".
    expect(optionValues(select)).toEqual(optionsBeforeFilter)
    expect(optionValues(select)).toEqual(expect.arrayContaining(['txt', 'png', 'java']))

    // Confirm the unfiltered "all extensions" query really was issued
    // (App.tsx's second, filter-independent query).
    expect(mockedListFiles).toHaveBeenCalledWith({})
  })
})
