import type { ColumnDef } from '@tanstack/react-table'
import type { FileRecord } from './types'

/**
 * Column structure for the file table (REQUIREMENTS.md section 3).
 *
 * "name" must always stay visible; the rest can be shown/hidden via
 * TanStack Table's column-visibility state (see App.tsx).
 *
 * Sorting/filtering behavior (section 4) is intentionally not wired up
 * yet — this is column structure only.
 */
export const columns: ColumnDef<FileRecord>[] = [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    enableHiding: false,
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
    header: 'Edited by',
  },
]
