import type { ColumnDef } from '@tanstack/react-table'
import type { FileRecord } from './types'
import type { SortOrder } from './api/types'

/**
 * Column structure for the file table (REQUIREMENTS.md section 3 & 4).
 *
 * "name" and "actions" always stay visible (`enableHiding: false`); the rest
 * can be shown/hidden via TanStack Table's column-visibility state (see
 * App.tsx).
 *
 * Sorting is server-side (REQUIREMENTS.md §4 "Sorting" — only "Edited by"
 * needs it): the "Edited by" header renders a button that cycles
 * none -> asc -> desc -> none via `onToggleEditedBySort`, which App.tsx
 * turns into `sort=editedBy&order=...` query params sent to the backend.
 * This file does not reimplement sorting/filtering itself.
 *
 * The "actions" column holds Download/Delete today; it's a dedicated column
 * (not folded into "name") so a future click-to-preview action on the name
 * cell can be added without touching this layout.
 *
 * The "name" cell is rendered as a clickable button when the file's
 * extension is previewable (REQUIREMENTS.md §2: .java/.png), calling
 * `onPreview`; otherwise it's plain text, per "structured displaying
 * contents is not required" for other types.
 */
export const PREVIEWABLE_EXTENSIONS = ['java', 'png']

export interface ColumnsOptions {
  editedBySortOrder: SortOrder | null
  onToggleEditedBySort: () => void
  onDownload: (id: number) => void
  onDelete: (id: number, name: string) => void
  onPreview: (file: FileRecord) => void
}

function sortIndicator(order: SortOrder | null): string {
  if (order === 'asc') return '↑'
  if (order === 'desc') return '↓'
  return '↕'
}

export function createColumns({
  editedBySortOrder,
  onToggleEditedBySort,
  onDownload,
  onDelete,
  onPreview,
}: ColumnsOptions): ColumnDef<FileRecord>[] {
  return [
    {
      id: 'name',
      accessorKey: 'name',
      header: 'Name',
      enableHiding: false,
      cell: ({ row }) => {
        const file = row.original
        if (!PREVIEWABLE_EXTENSIONS.includes(file.extension)) {
          return file.name
        }
        return (
          <button
            type="button"
            className="name-preview-link"
            onClick={() => onPreview(file)}
          >
            {file.name}
          </button>
        )
      },
    },
    {
      id: 'createdAt',
      accessorKey: 'createdAt',
      header: 'Created',
    },
    {
      id: 'modifiedAt',
      accessorKey: 'modifiedAt',
      header: 'Modified',
    },
    {
      id: 'uploadedBy',
      accessorKey: 'uploadedBy',
      header: 'Uploaded by',
    },
    {
      id: 'editedBy',
      accessorKey: 'editedBy',
      header: () => (
        <button
          type="button"
          className="sort-toggle"
          onClick={onToggleEditedBySort}
          aria-label={`Sort by edited by (currently ${editedBySortOrder ?? 'unsorted'})`}
        >
          Edited by {sortIndicator(editedBySortOrder)}
        </button>
      ),
    },
    {
      id: 'actions',
      header: 'Actions',
      enableHiding: false,
      cell: ({ row }) => (
        <div className="row-actions">
          <button type="button" onClick={() => onDownload(row.original.id)}>
            Download
          </button>
          <button
            type="button"
            onClick={() => onDelete(row.original.id, row.original.name)}
          >
            Delete
          </button>
        </div>
      ),
    },
  ]
}
