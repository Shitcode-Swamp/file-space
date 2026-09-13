import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ACCESS_TOKEN_KEY,
  REFRESH_TOKEN_KEY,
  USERNAME_KEY,
  getFile,
  listFiles,
  register,
} from './client'
import { ApiError } from './types'

/** Builds a real Response object (jsdom/Node both provide the constructor). */
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function headerOf(init: unknown, name: string): string | undefined {
  const headers = (init as { headers?: Record<string, string> } | undefined)?.headers
  return headers?.[name]
}

describe('api/client', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('attaches Authorization: Bearer <token> on an authenticated GET when an access token is stored', async () => {
    localStorage.setItem(ACCESS_TOKEN_KEY, 'access-123')

    const fetchMock = vi.fn().mockResolvedValue(jsonResponse([]))
    vi.stubGlobal('fetch', fetchMock)

    await listFiles()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(headerOf(init, 'Authorization')).toBe('Bearer access-123')
  })

  it('refreshes exactly once on a 401 and retries the original request with the new access token', async () => {
    localStorage.setItem(ACCESS_TOKEN_KEY, 'expired-token')
    localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-token')

    const fetchMock = vi.fn()
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ error: 'unauthorized' }, 401)) // original request
      .mockResolvedValueOnce(jsonResponse({ accessToken: 'new-token' })) // refresh
      .mockResolvedValueOnce(jsonResponse({ id: 1, name: 'a.txt' })) // retried original request
    vi.stubGlobal('fetch', fetchMock)

    const result = await getFile(1)

    expect(fetchMock).toHaveBeenCalledTimes(3)

    // Exactly one refresh call, to the refresh endpoint.
    const refreshCall = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(refreshCall[0]).toContain('/api/auth/refresh')

    // The retry hits the same original path...
    const retryCall = fetchMock.mock.calls[2] as [string, RequestInit]
    expect(retryCall[0]).toContain('/api/files/1')
    // ...bearing the new access token.
    expect(headerOf(retryCall[1], 'Authorization')).toBe('Bearer new-token')

    // The retry's result is what's returned to the caller.
    expect(result).toEqual({ id: 1, name: 'a.txt' })

    // The refreshed token is persisted.
    expect(localStorage.getItem(ACCESS_TOKEN_KEY)).toBe('new-token')
  })

  it('clears stored tokens and propagates the original 401 when the refresh call itself fails', async () => {
    localStorage.setItem(ACCESS_TOKEN_KEY, 'expired-token')
    localStorage.setItem(REFRESH_TOKEN_KEY, 'bad-refresh-token')
    localStorage.setItem(USERNAME_KEY, 'alice')

    const fetchMock = vi.fn()
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ error: 'unauthorized' }, 401)) // original request
      .mockResolvedValueOnce(jsonResponse({ error: 'invalid refresh token' }, 401)) // refresh fails
    vi.stubGlobal('fetch', fetchMock)

    const err: unknown = await listFiles().catch((e: unknown) => e)

    // No infinite retry loop: exactly the original call + the one refresh attempt.
    expect(fetchMock).toHaveBeenCalledTimes(2)

    // The original 401 propagates as a real error, not swallowed.
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    expect((err as ApiError).message).toBe('unauthorized')

    // Tokens are wiped.
    expect(localStorage.getItem(ACCESS_TOKEN_KEY)).toBeNull()
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull()
    expect(localStorage.getItem(USERNAME_KEY)).toBeNull()
  })

  it('decodes a successful 2xx JSON response', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse([{ id: 7, name: 'doc.txt' }]))
    vi.stubGlobal('fetch', fetchMock)

    const result = await listFiles()

    expect(result).toEqual([{ id: 7, name: 'doc.txt' }])
  })

  it('throws an ApiError carrying the server error message for a non-401 failure', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ error: 'username taken' }, 409))
    vi.stubGlobal('fetch', fetchMock)

    const err: unknown = await register('alice', 'hunter2').catch((e: unknown) => e)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).message).toBe('username taken')
  })
})
