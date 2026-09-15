import type {
  ApiFileRecord,
  ListFilesParams,
  ListFilesResult,
  LoginResponse,
  RefreshResponse,
  RegisterResponse,
} from './types'
import { ApiError } from './types'

/**
 * Shared localStorage keys.
 *
 * IMPORTANT: these must stay in sync with src/auth/AuthContext.tsx, which is
 * the other reader/writer of these keys. This file reads tokens directly out
 * of localStorage (rather than importing from AuthContext) to avoid a
 * circular dependency between plain module code and a React context.
 */
export const ACCESS_TOKEN_KEY = 'filespace.accessToken'
export const REFRESH_TOKEN_KEY = 'filespace.refreshToken'
export const USERNAME_KEY = 'filespace.username'

const BASE_URL: string =
  (import.meta.env.VITE_API_URL as string | undefined) ?? 'http://localhost:8080'

function getAccessToken(): string | null {
  return localStorage.getItem(ACCESS_TOKEN_KEY)
}

function getRefreshToken(): string | null {
  return localStorage.getItem(REFRESH_TOKEN_KEY)
}

function setAccessToken(token: string): void {
  localStorage.setItem(ACCESS_TOKEN_KEY, token)
}

function clearTokens(): void {
  localStorage.removeItem(ACCESS_TOKEN_KEY)
  localStorage.removeItem(REFRESH_TOKEN_KEY)
  localStorage.removeItem(USERNAME_KEY)
  unauthorizedListener?.()
}

/** Exposed so the auth layer can clear the session on an explicit logout(). */
export { clearTokens }

/**
 * Optional hook the auth layer can register to be notified the moment this
 * module clears tokens out from under it (i.e. a 401 that survived a refresh
 * attempt). Lets AuthContext update its in-memory state immediately instead
 * of waiting for a reload. See AuthContext.tsx.
 */
let unauthorizedListener: (() => void) | null = null

export function setUnauthorizedListener(listener: (() => void) | null): void {
  unauthorizedListener = listener
}

interface RequestOptions {
  method?: string
  body?: unknown
  isFormData?: boolean
}

async function parseErrorMessage(res: Response): Promise<string> {
  try {
    const data: unknown = await res.json()
    if (data && typeof data === 'object' && 'error' in data) {
      const err = (data as { error?: unknown }).error
      if (typeof err === 'string') return err
    }
  } catch {
    // response body wasn't JSON — fall through to the generic message
  }
  return `Request failed with status ${res.status}`
}

/** Performs one HTTP call, attaching the bearer token when present. */
async function rawFetch(path: string, options: RequestOptions, token: string | null): Promise<Response> {
  const headers: Record<string, string> = {}
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }

  let body: BodyInit | undefined
  if (options.body !== undefined) {
    if (options.isFormData) {
      body = options.body as FormData
      // Let the browser set Content-Type (with boundary) for multipart bodies.
    } else {
      headers['Content-Type'] = 'application/json'
      body = JSON.stringify(options.body)
    }
  }

  return fetch(`${BASE_URL}${path}`, {
    method: options.method ?? 'GET',
    headers,
    body,
  })
}

/**
 * Tries to refresh the access token using the stored refresh token. On
 * success, stores and returns the new access token; on failure, clears all
 * stored tokens and returns null.
 */
async function tryRefresh(): Promise<string | null> {
  const refreshToken = getRefreshToken()
  if (!refreshToken) {
    clearTokens()
    return null
  }

  try {
    const res = await fetch(`${BASE_URL}/api/auth/refresh`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refreshToken }),
    })
    if (!res.ok) {
      clearTokens()
      return null
    }
    const data = (await res.json()) as RefreshResponse
    setAccessToken(data.accessToken)
    return data.accessToken
  } catch {
    clearTokens()
    return null
  }
}

/**
 * Core request path shared by the JSON and blob helpers below: attaches the
 * stored access token, and on a 401 that was returned to an authenticated
 * request, refreshes exactly once and retries exactly once.
 */
async function requestRaw(path: string, options: RequestOptions = {}): Promise<Response> {
  const token = getAccessToken()
  const res = await rawFetch(path, options, token)

  if (res.status === 401 && token) {
    const newToken = await tryRefresh()
    if (newToken) {
      return rawFetch(path, options, newToken)
    }
  }

  return res
}

async function requestJson<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const res = await requestRaw(path, options)
  if (!res.ok) {
    throw new ApiError(res.status, await parseErrorMessage(res))
  }
  if (res.status === 204) {
    return undefined as T
  }
  return (await res.json()) as T
}

async function requestBlob(path: string): Promise<{ blob: Blob; filename: string | null }> {
  const res = await requestRaw(path)
  if (!res.ok) {
    throw new ApiError(res.status, await parseErrorMessage(res))
  }
  const blob = await res.blob()
  const disposition = res.headers.get('Content-Disposition')
  const filename = parseFilename(disposition)
  return { blob, filename }
}

function parseFilename(disposition: string | null): string | null {
  if (!disposition) return null
  // Prefer the RFC 6266 filename* form: it's percent-encoded UTF-8, so it
  // survives non-ASCII names that the plain filename="..." fallback (sent
  // as raw bytes, and Latin-1-decoded by the browser's Headers API) would
  // otherwise mangle.
  const extended = /filename\*=UTF-8''([^;]+)/i.exec(disposition)
  if (extended) return decodeURIComponent(extended[1])
  const plain = /filename="?([^";]+)"?/i.exec(disposition)
  return plain ? plain[1] : null
}

// ---- Public API surface -------------------------------------------------

export async function register(username: string, password: string): Promise<RegisterResponse> {
  return requestJson<RegisterResponse>('/api/auth/register', {
    method: 'POST',
    body: { username, password },
  })
}

export async function login(username: string, password: string): Promise<LoginResponse> {
  return requestJson<LoginResponse>('/api/auth/login', {
    method: 'POST',
    body: { username, password },
  })
}

export async function refresh(refreshToken: string): Promise<RefreshResponse> {
  return requestJson<RefreshResponse>('/api/auth/refresh', {
    method: 'POST',
    body: { refreshToken },
  })
}

export async function listFiles(params: ListFilesParams = {}): Promise<ListFilesResult> {
  const search = new URLSearchParams()
  if (params.sort) search.set('sort', params.sort)
  if (params.order) search.set('order', params.order)
  if (params.extension) search.set('extension', params.extension)
  if (params.limit !== undefined) search.set('limit', String(params.limit))
  if (params.offset !== undefined) search.set('offset', String(params.offset))
  const qs = search.toString()

  const res = await requestRaw(`/api/files${qs ? `?${qs}` : ''}`)
  if (!res.ok) {
    throw new ApiError(res.status, await parseErrorMessage(res))
  }
  const files = (await res.json()) as ApiFileRecord[]
  // Cross-origin fetch() hides response headers not explicitly exposed by
  // the server's CORS config; see main.go's ExposedHeaders.
  const hasMore = res.headers.get('X-Has-More') === 'true'
  return { files, hasMore }
}

export async function getFile(id: number): Promise<ApiFileRecord> {
  return requestJson<ApiFileRecord>(`/api/files/${id}`)
}

/** Inline preview bytes for .java/.png files; 415 (as ApiError) for anything else. */
export async function fetchFileContent(id: number): Promise<Blob> {
  const { blob } = await requestBlob(`/api/files/${id}/content`)
  return blob
}

export async function downloadFile(id: number): Promise<{ blob: Blob; filename: string | null }> {
  return requestBlob(`/api/files/${id}/download`)
}

interface InitiateUploadResponse {
  uploadId: string
  chunkSize: number
}

/** Starts a chunked-upload session for a file of the given declared size. */
async function initiateUpload(filename: string, size: number): Promise<InitiateUploadResponse> {
  return requestJson<InitiateUploadResponse>('/api/files/uploads', {
    method: 'POST',
    body: { filename, size },
  })
}

/**
 * Uploads one chunk, reporting (bytesSent, chunk.size) as the browser
 * actually flushes it over the wire. This is the one place this module uses
 * XMLHttpRequest instead of fetch(): fetch has no upload-progress event, only
 * XHR's `upload.onprogress` does, and progress feedback is the whole point
 * of chunking a big upload in the first place.
 */
async function uploadChunk(
  uploadId: string,
  index: number,
  chunk: Blob,
  onProgress?: (loaded: number, total: number) => void,
): Promise<void> {
  const path = `/api/files/uploads/${uploadId}/chunks/${index}`

  const send = (token: string | null) =>
    new Promise<XMLHttpRequest>((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      xhr.open('PUT', `${BASE_URL}${path}`)
      if (token) xhr.setRequestHeader('Authorization', `Bearer ${token}`)
      if (onProgress) {
        xhr.upload.onprogress = (e) => {
          if (e.lengthComputable) onProgress(e.loaded, e.total)
        }
      }
      xhr.onload = () => resolve(xhr)
      xhr.onerror = () => reject(new Error('network error uploading chunk'))
      xhr.send(chunk)
    })

  const token = getAccessToken()
  let xhr = await send(token)

  // Same one-refresh-one-retry contract as requestRaw, just reimplemented
  // for XHR since it can't share fetch's Response-based code path.
  if (xhr.status === 401 && token) {
    const newToken = await tryRefresh()
    if (newToken) {
      xhr = await send(newToken)
    }
  }

  if (xhr.status < 200 || xhr.status >= 300) {
    let message = `Request failed with status ${xhr.status}`
    try {
      const parsed = JSON.parse(xhr.responseText) as { error?: string }
      if (parsed.error) message = parsed.error
    } catch {
      // response body wasn't JSON — keep the generic message
    }
    throw new ApiError(xhr.status, message)
  }
}

async function completeUpload(uploadId: string): Promise<ApiFileRecord> {
  return requestJson<ApiFileRecord>(`/api/files/uploads/${uploadId}/complete`, { method: 'POST' })
}

async function abortUpload(uploadId: string): Promise<void> {
  await requestJson<void>(`/api/files/uploads/${uploadId}`, { method: 'DELETE' })
}

/**
 * Files at or below this size upload via the original single-shot
 * multipart endpoint (1 request) instead of the chunked-upload session
 * protocol (initiate + chunk + complete = 3 requests at minimum). Chunking
 * exists to keep large uploads under Cloudflare's per-request body-size
 * limit — a file that already fits in a single chunk was never at risk of
 * hitting that limit, so paying the extra round trips for it is pure
 * overhead. Matches backend/internal/service/uploads.go's UploadChunkSize.
 *
 * Mutable (rather than a const) only so tests can force small fixture
 * files through the chunked path via setSingleShotUploadThresholdForTests,
 * instead of needing multi-megabyte fixtures to naturally clear it.
 * Application code never changes it.
 */
let singleShotUploadThreshold = 8 << 20 // 8 MiB

/** Test-only seam — see singleShotUploadThreshold. Not used by application code. */
export function setSingleShotUploadThresholdForTests(bytes: number): void {
  singleShotUploadThreshold = bytes
}

/** Original single-shot upload path — one request, used for files at or
 * below singleShotUploadThreshold. */
async function uploadSingleShot(file: File): Promise<ApiFileRecord> {
  const formData = new FormData()
  formData.append('file', file)
  return requestJson<ApiFileRecord>('/api/files', {
    method: 'POST',
    body: formData,
    isFormData: true,
  })
}

/**
 * Uploads file in fixed-size chunks (the server tells us the size —
 * currently 8MiB, see backend/internal/service/uploads.go), so a single
 * request is never more than a few MB regardless of the file's total size.
 * That matters in production: file-space sits behind a Cloudflare Tunnel
 * (REQUIREMENTS.md §5.6), whose edge rejects large single-request bodies
 * outright, and it's what makes onProgress meaningful for genuinely large
 * files instead of just "0% ... 100%". Files small enough to need only one
 * chunk anyway skip this session protocol entirely — see
 * singleShotUploadThreshold.
 */
export async function uploadFileInChunks(
  file: File,
  onProgress?: (loaded: number, total: number) => void,
): Promise<ApiFileRecord> {
  if (file.size <= singleShotUploadThreshold) {
    const created = await uploadSingleShot(file)
    onProgress?.(file.size, file.size)
    return created
  }

  const { uploadId, chunkSize } = await initiateUpload(file.name, file.size)

  try {
    let uploaded = 0
    let index = 0
    for (let offset = 0; offset < file.size; offset += chunkSize) {
      const chunk = file.slice(offset, offset + chunkSize)
      const uploadedBeforeThisChunk = uploaded
      await uploadChunk(uploadId, index, chunk, (loaded) => {
        onProgress?.(uploadedBeforeThisChunk + loaded, file.size)
      })
      uploaded += chunk.size
      index += 1
    }
    return await completeUpload(uploadId)
  } catch (err) {
    // Best-effort: free the server-side scratch file/session now rather
    // than waiting for its idle timeout. A failure here doesn't change what
    // gets thrown — the upload already failed for its own reason.
    await abortUpload(uploadId).catch(() => {})
    throw err
  }
}

export async function deleteFile(id: number): Promise<void> {
  await requestJson<void>(`/api/files/${id}`, { method: 'DELETE' })
}
