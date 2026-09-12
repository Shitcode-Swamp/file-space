/**
 * A single remote file entry, as returned by GET /api/files.
 * Mirrors the columns in REQUIREMENTS.md section 3.
 *
 * Field set matches `ApiFileRecord` in src/api/types.ts (the backend now
 * resolves uploadedBy/editedBy to usernames, and `id` is a JSON number, not
 * a string). Kept as a separate declaration rather than a re-export so the
 * UI/table layer doesn't take a hard dependency on the API layer's module.
 */
export interface FileRecord {
  id: number
  name: string
  extension: string
  size: number
  createdAt: string
  modifiedAt: string
  uploadedBy: string
  editedBy: string
}
