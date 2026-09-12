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

export interface ListFilesParams {
  sort?: 'editedBy'
  order?: SortOrder
  extension?: string
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
