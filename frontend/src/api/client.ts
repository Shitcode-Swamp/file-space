import type {
  ApiFileRecord,
  ListFilesParams,
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
  const match = /filename\*?=(?:UTF-8'')?"?([^";]+)"?/i.exec(disposition)
  return match ? decodeURIComponent(match[1]) : null
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

export async function listFiles(params: ListFilesParams = {}): Promise<ApiFileRecord[]> {
  const search = new URLSearchParams()
  if (params.sort) search.set('sort', params.sort)
  if (params.order) search.set('order', params.order)
  if (params.extension) search.set('extension', params.extension)
  const qs = search.toString()
  return requestJson<ApiFileRecord[]>(`/api/files${qs ? `?${qs}` : ''}`)
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

export async function uploadFile(file: File): Promise<ApiFileRecord> {
  const formData = new FormData()
  formData.append('file', file)
  return requestJson<ApiFileRecord>('/api/files', {
    method: 'POST',
    body: formData,
    isFormData: true,
  })
}

export async function deleteFile(id: number): Promise<void> {
  await requestJson<void>(`/api/files/${id}`, { method: 'DELETE' })
}
