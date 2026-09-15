/**
 * Local API-layer types. Deliberately separate from ../types.ts (which
 * belongs to the table/UI layer) so this module has no dependency on it.
 */

/** A file entry as returned by the Go API (GET /api/files, /api/files/{id}, POST /api/files). */
export interface ApiFileRecord {
  id: number
  name: string
  extension: string
  size: number
  createdAt: string
  modifiedAt: string
  uploadedBy: string
  editedBy: string
}

export interface AuthTokens {
  accessToken: string
  refreshToken: string
}

export interface RegisterResponse {
  id: number
  username: string
}

export interface LoginResponse extends AuthTokens {}

export interface RefreshResponse {
  accessToken: string
}

export type SortOrder = 'asc' | 'desc'
export type SortField = 'name' | 'createdAt' | 'modifiedAt' | 'uploadedBy' | 'editedBy'

export interface ListFilesParams {
  sort?: SortField
  order?: SortOrder
  extension?: string
  /** Page size. The backend defaults this to 50 and caps it at 1000. */
  limit?: number
  offset?: number
}

/** GET /api/files's bare JSON array, plus whether more pages exist — the
 * latter comes back as the X-Has-More response header (see client.ts),
 * not part of the JSON body. */
export interface ListFilesResult {
  files: ApiFileRecord[]
  hasMore: boolean
}

/** Shape of the JSON error body the API sends alongside 4xx/5xx statuses. */
export interface ApiErrorBody {
  error: string
}

/** Thrown by the api client whenever a request completes with a non-2xx status. */
export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}
