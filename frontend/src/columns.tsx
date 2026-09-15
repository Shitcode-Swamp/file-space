import type { ColumnDef } from '@tanstack/react-table'
import type { FileRecord } from './types'

/**
 * Column structure for the file table (REQUIREMENTS.md section 3 & 4).
 *
 * "name" and "actions" always stay visible (`enableHiding: false`); the rest
 * can be shown/hidden via TanStack Table's column-visibility state (see
 * App.tsx).
 *
 * Sorting is client-side, via TanStack Table's built-in sorting model (see
 * App.tsx's `sorting` state / `getSortedRowModel`): every data column here
 * is sortable by clicking its header, name is the default sort, and
 * App.tsx renders the clickable header + indicator generically rather than
 * this file hand-rolling a toggle per column.
 *
 * The "actions" column holds Download/Delete today; it's a dedicated column
 * (not folded into "name") so a future click-to-preview action on the name
 * cell can be added without touching this layout. It opts out of sorting
 * since it has no backing data field.
 *
 * The "name" cell is rendered as a clickable button when the file's
 * extension is previewable (REQUIREMENTS.md §2: .java/.png), calling
 * `onPreview`; otherwise it's plain text, per "structured displaying
 * contents is not required" for other types.
 */
export const PREVIEWABLE_EXTENSIONS = ['java', 'png']

export interface ColumnsOptions {
  onDownload: (id: number) => void
  onDelete: (id: number, name: string) => void
  onPreview: (file: FileRecord) => void
}

// The backend sends createdAt/modifiedAt as ISO 8601 UTC timestamps; render
// them in the viewer's own timezone as DD/MM/YYYY, 24-hour time — a fixed
// format (via 'en-GB') rather than one that shifts with the viewer's locale
// (e.g. MM/DD/YYYY + AM/PM under 'en-US').
const dateTimeFormatter = new Intl.DateTimeFormat('en-GB', {
  day: '2-digit',
  month: '2-digit',
  year: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
})

function formatDateTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return dateTimeFormatter.format(date)
}

export function createColumns({
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
      cell: ({ getValue }) => formatDateTime(getValue<string>()),
    },
    {
      id: 'modifiedAt',
      accessorKey: 'modifiedAt',
      header: 'Modified',
      cell: ({ getValue }) => formatDateTime(getValue<string>()),
    },
    {
      id: 'uploadedBy',
      accessorKey: 'uploadedBy',
      header: 'Uploaded by',
    },
    {
      id: 'editedBy',
      accessorKey: 'editedBy',
      header: 'Edited by',
    },
    {
      id: 'actions',
      header: 'Actions',
      enableHiding: false,
      enableSorting: false,
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
