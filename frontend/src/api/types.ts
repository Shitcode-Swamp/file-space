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

// ---- Sync (POST /api/sync/diff) -----------------------------------------
// Mirrors backend/internal/handler/sync.go's wire shape exactly, and the
// same protocol the Swift desktop client speaks
// (desktop/Sources/FileSpaceDesktop/Models.swift's SyncManifestEntryDTO /
// SyncDiffResponse / SyncActionDTO / SyncConflictDetailsDTO).

/** Per-file manifest entry sent with every sync/diff request (REQUIREMENTS.md
 * §6.1/§6.6). mtime is carried purely to match the wire shape — the server
 * doesn't use it for conflict detection, only name + sha256 do. */
export interface SyncManifestEntry {
  name: string
  size: number
  mtime: string
  sha256: string
}

export const SyncActionKind = {
  download: 'download',
  deleteLocal: 'delete_local',
  conflict: 'conflict',
} as const

export type SyncActionKindValue = (typeof SyncActionKind)[keyof typeof SyncActionKind]

/** Only present when action === 'conflict' — everything the resolution UI
 * needs to show the incoming server-side version alongside the client's own
 * local one, without a second round trip. */
export interface SyncConflictDetails {
  remoteSize: number
  remoteModifiedAt: string
  remoteEditedBy: string
}

export interface SyncAction {
  name: string
  action: SyncActionKindValue
  remoteVersion: number
  conflict?: SyncConflictDetails
}

export interface SyncDiffResponse {
  newVersion: number
  actions: SyncAction[]
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
