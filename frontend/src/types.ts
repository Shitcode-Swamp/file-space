/**
 * A single remote file entry, as returned by GET /api/files.
 * Mirrors the columns in REQUIREMENTS.md section 3.
 */
export interface FileRecord {
  id: string
  name: string
  createdAt: string
  modifiedAt: string
  uploadedBy: string
  editedBy: string
}
