import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ACCESS_TOKEN_KEY,
  REFRESH_TOKEN_KEY,
  USERNAME_KEY,
  getFile,
  listFiles,
  register,
  setSingleShotUploadThresholdForTests,
  uploadFileInChunks,
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

  it('decodes a successful 2xx JSON response, reading hasMore off the X-Has-More header', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify([{ id: 7, name: 'doc.txt' }]), {
        status: 200,
        headers: { 'Content-Type': 'application/json', 'X-Has-More': 'true' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const result = await listFiles()

    expect(result).toEqual({ files: [{ id: 7, name: 'doc.txt' }], hasMore: true })
  })

  it('defaults hasMore to false when the X-Has-More header is absent', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse([{ id: 7, name: 'doc.txt' }]))
    vi.stubGlobal('fetch', fetchMock)

    const result = await listFiles()

    expect(result).toEqual({ files: [{ id: 7, name: 'doc.txt' }], hasMore: false })
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

/**
 * Minimal fake standing in for the real XMLHttpRequest, since uploadChunk
 * uses it (not fetch) to get upload-progress events. `send()` resolves
 * synchronously against `FakeXHR.respondWith`, firing `upload.onprogress`
 * first (as the real browser would, for a fully-buffered chunk) and then
 * `onload`.
 */
class FakeXHR {
  static sent: FakeXHR[] = []
  static respondWith: { status: number; body?: string } = { status: 204 }

  method = ''
  url = ''
  status = 0
  responseText = ''
  headers: Record<string, string> = {}
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } =
    { onprogress: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null

  open(method: string, url: string) {
    this.method = method
    this.url = url
  }

  setRequestHeader(name: string, value: string) {
    this.headers[name] = value
  }

  send(body: Blob) {
    FakeXHR.sent.push(this)
    const { status, body: respBody } = FakeXHR.respondWith
    this.status = status
    this.responseText = respBody ?? ''
    this.upload.onprogress?.({ lengthComputable: true, loaded: body.size, total: body.size })
    this.onload?.()
  }
}

describe('api/client uploadFileInChunks', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem(ACCESS_TOKEN_KEY, 'access-123')
    FakeXHR.sent = []
    FakeXHR.respondWith = { status: 204 }
    vi.stubGlobal('XMLHttpRequest', FakeXHR)
    // Force even these tests' tiny (including zero-byte) fixture files
    // through the chunked-upload path instead of legitimately qualifying
    // for the single-shot fast path (see the dedicated describe block
    // below for that path's own coverage). -1 rather than 0: a 0-byte file
    // would otherwise satisfy "size <= 0" and take the fast path too.
    setSingleShotUploadThresholdForTests(-1)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
    setSingleShotUploadThresholdForTests(8 << 20)
  })

  it('splits the file into chunkSize pieces, reports progress per chunk, then completes the session', async () => {
    const fetchMock = vi.fn()
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ uploadId: 'up-1', chunkSize: 4 })) // initiate
      .mockResolvedValueOnce(jsonResponse({ id: 9, name: 'big.bin' })) // complete
    vi.stubGlobal('fetch', fetchMock)

    const file = new File([new Uint8Array(10)], 'big.bin')
    const progressCalls: Array<[number, number]> = []

    const result = await uploadFileInChunks(file, (loaded, total) => progressCalls.push([loaded, total]))

    expect(result).toEqual({ id: 9, name: 'big.bin' })
    // 10 bytes at chunkSize 4 -> chunks of 4, 4, 2 bytes.
    expect(FakeXHR.sent.map((x) => x.url)).toEqual([
      expect.stringContaining('/api/files/uploads/up-1/chunks/0'),
      expect.stringContaining('/api/files/uploads/up-1/chunks/1'),
      expect.stringContaining('/api/files/uploads/up-1/chunks/2'),
    ])
    expect(FakeXHR.sent.every((x) => x.method === 'PUT')).toBe(true)
    expect(FakeXHR.sent.every((x) => x.headers['Authorization'] === 'Bearer access-123')).toBe(true)
    expect(progressCalls).toEqual([
      [4, 10],
      [8, 10],
      [10, 10],
    ])

    // The finalize call after every chunk.
    const completeCall = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(completeCall[0]).toContain('/api/files/uploads/up-1/complete')
  })

  it('aborts the upload session when a chunk fails, and still rejects with that error', async () => {
    const fetchMock = vi.fn()
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ uploadId: 'up-2', chunkSize: 4 })) // initiate
      .mockResolvedValueOnce(new Response(null, { status: 204 })) // abort (DELETE)
    vi.stubGlobal('fetch', fetchMock)
    FakeXHR.respondWith = { status: 500, body: JSON.stringify({ error: 'disk full' }) }

    const file = new File([new Uint8Array(10)], 'big.bin')

    const err: unknown = await uploadFileInChunks(file).catch((e: unknown) => e)

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).message).toBe('disk full')

    // initiate, then abort -- the failed chunk short-circuits the loop, and
    // complete is never reached.
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const abortCall = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(abortCall[0]).toContain('/api/files/uploads/up-2')
    expect(abortCall[1].method).toBe('DELETE')
  })

  it('refreshes the token once and retries the same chunk on a 401', async () => {
    localStorage.setItem(REFRESH_TOKEN_KEY, 'refresh-1')
    const fetchMock = vi.fn()
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ uploadId: 'up-3', chunkSize: 100 })) // initiate
      .mockResolvedValueOnce(jsonResponse({ accessToken: 'fresh-token' })) // refresh
      .mockResolvedValueOnce(jsonResponse({ id: 1, name: 'f.txt' })) // complete
    vi.stubGlobal('fetch', fetchMock)

    let sendCount = 0
    class RetryOnceXHR extends FakeXHR {
      send(body: Blob) {
        sendCount++
        FakeXHR.respondWith = sendCount === 1 ? { status: 401 } : { status: 204 }
        super.send(body)
      }
    }
    vi.stubGlobal('XMLHttpRequest', RetryOnceXHR)

    const file = new File([new Uint8Array(5)], 'f.txt')
    await uploadFileInChunks(file)

    // The one chunk was sent twice: the 401, then the retry with the new token.
    expect(FakeXHR.sent).toHaveLength(2)
    expect(FakeXHR.sent[0].headers['Authorization']).toBe('Bearer access-123')
    expect(FakeXHR.sent[1].headers['Authorization']).toBe('Bearer fresh-token')
  })

  it('completes immediately for a zero-byte file, sending no chunks', async () => {
    const fetchMock = vi.fn()
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ uploadId: 'up-4', chunkSize: 4 })) // initiate
      .mockResolvedValueOnce(jsonResponse({ id: 2, name: 'empty.txt' })) // complete
    vi.stubGlobal('fetch', fetchMock)

    const result = await uploadFileInChunks(new File([], 'empty.txt'))

    expect(FakeXHR.sent).toHaveLength(0)
    expect(result).toEqual({ id: 2, name: 'empty.txt' })
  })
})

describe('api/client uploadFileInChunks single-shot fast path', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem(ACCESS_TOKEN_KEY, 'access-123')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  // Pins the fix this was written for: a file at or below the default
  // 8MiB threshold must go through the original single-shot multipart
  // endpoint (one request) rather than the chunked-upload session protocol
  // (initiate + chunk + complete = three requests), which used to run
  // unconditionally and made folder sync's "re-upload a changed file on
  // every save" noticeably slower for ordinary source-sized files.
  it('uploads a small file via a single multipart request, not the chunked session', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ id: 9, name: 'notes.txt' }, 201))
    vi.stubGlobal('fetch', fetchMock)

    const progressCalls: Array<[number, number]> = []
    const file = new File([new Uint8Array(10)], 'notes.txt')
    const result = await uploadFileInChunks(file, (loaded, total) => progressCalls.push([loaded, total]))

    expect(result).toEqual({ id: 9, name: 'notes.txt' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [calledUrl, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(calledUrl).toContain('/api/files')
    expect(calledUrl).not.toContain('/uploads')
    expect(init.method).toBe('POST')
    expect(init.body).toBeInstanceOf(FormData)

    // A single "fully done" progress callback, not a per-chunk trickle.
    expect(progressCalls).toEqual([[10, 10]])
  })
})
