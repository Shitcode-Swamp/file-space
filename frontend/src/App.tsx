import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  getCoreRowModel,
  useReactTable,
  flexRender,
  type VisibilityState,
} from '@tanstack/react-table'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createColumns } from './columns'
import type { FileRecord } from './types'
import type { SortOrder } from './api/types'
import * as api from './api/client'
import { useAuth } from './auth/AuthContext'
import { LoginPage } from './auth/LoginPage'
import { FilePreviewModal, type FilePreviewState } from './FilePreviewModal'
import './App.css'

function DriveView() {
  const { user, logout } = useAuth()
  const queryClient = useQueryClient()

  const [columnVisibility, setColumnVisibility] = useState<VisibilityState>({})
  const [editedBySortOrder, setEditedBySortOrder] = useState<SortOrder | null>(null)
  const [extensionFilter, setExtensionFilter] = useState<string | null>(null)
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [preview, setPreview] = useState<FilePreviewState | null>(null)

  // The table's own data: sorted/filtered server-side per the current
  // controls. Refetches whenever sort/filter state changes.
  const filesQuery = useQuery({
    queryKey: ['files', { sort: editedBySortOrder, extension: extensionFilter }],
    queryFn: () =>
      api.listFiles({
        ...(editedBySortOrder ? { sort: 'editedBy', order: editedBySortOrder } : {}),
        ...(extensionFilter ? { extension: extensionFilter } : {}),
      }),
  })

  // A second, filter-independent fetch used only to populate the extension
  // dropdown's option list. Using the same (filtered) query for this would
  // make the dropdown collapse to just the selected extension once a filter
  // is applied, since the backend already excludes everything else.
  const allFilesQuery = useQuery({
    queryKey: ['files', 'all-extensions'],
    queryFn: () => api.listFiles({}),
  })

  const extensionOptions = useMemo(() => {
    const set = new Set<string>()
    for (const file of allFilesQuery.data ?? []) {
      if (file.extension) set.add(file.extension)
    }
    return Array.from(set).sort()
  }, [allFilesQuery.data])

  const uploadMutation = useMutation({
    mutationFn: (file: File) => api.uploadFile(file),
    onSuccess: () => {
      setActionError(null)
      setSelectedFile(null)
      void queryClient.invalidateQueries({ queryKey: ['files'] })
    },
    onError: (err: unknown) => {
      setActionError(err instanceof Error ? err.message : 'Upload failed')
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.deleteFile(id),
    onSuccess: () => {
      setActionError(null)
      void queryClient.invalidateQueries({ queryKey: ['files'] })
    },
    onError: (err: unknown) => {
      setActionError(err instanceof Error ? err.message : 'Delete failed')
    },
  })

  const handleDownload = useCallback(async (id: number) => {
    try {
      const { blob, filename } = await api.downloadFile(id)
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = filename ?? `file-${id}`
      document.body.appendChild(link)
      link.click()
      link.remove()
      URL.revokeObjectURL(url)
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Download failed')
    }
  }, [])

  const deleteFile = deleteMutation.mutate
  const handleDelete = useCallback(
    (id: number, name: string) => {
      if (!window.confirm(`Delete "${name}"? This cannot be undone.`)) return
      deleteFile(id)
    },
    [deleteFile],
  )

  const toggleEditedBySort = useCallback(() => {
    setEditedBySortOrder((prev) => (prev === null ? 'asc' : prev === 'asc' ? 'desc' : null))
  }, [])

  // Revoke the previous preview's object URL (if any) whenever the preview
  // state changes away from it or the component unmounts, so we never leak
  // blob URLs. Runs on every `preview` change since the cleanup fires before
  // the *next* effect too, not just on unmount.
  useEffect(() => {
    return () => {
      if (preview?.status === 'image') {
        URL.revokeObjectURL(preview.objectUrl)
      }
    }
  }, [preview])

  const closePreview = useCallback(() => setPreview(null), [])

  const handlePreview = useCallback(async (file: FileRecord) => {
    setPreview({ status: 'loading', file })
    try {
      const blob = await api.fetchFileContent(file.id)
      if (file.extension === 'png') {
        const objectUrl = URL.createObjectURL(blob)
        setPreview({ status: 'image', file, objectUrl })
      } else {
        const content = await blob.text()
        setPreview({ status: 'text', file, content })
      }
    } catch (err) {
      setPreview({
        status: 'error',
        file,
        message: err instanceof Error ? err.message : 'Failed to load preview',
      })
    }
  }, [])

  const columns = useMemo(
    () =>
      createColumns({
        editedBySortOrder,
        onToggleEditedBySort: toggleEditedBySort,
        onDownload: (id) => void handleDownload(id),
        onDelete: handleDelete,
        onPreview: (file) => void handlePreview(file),
      }),
    [editedBySortOrder, toggleEditedBySort, handleDownload, handleDelete, handlePreview],
  )

  const data: FileRecord[] = filesQuery.data ?? []

  const table = useReactTable({
    data,
    columns,
    state: {
      columnVisibility,
    },
    onColumnVisibilityChange: setColumnVisibility,
    getCoreRowModel: getCoreRowModel(),
  })

  const handleUploadClick = () => {
    if (!selectedFile) return
    uploadMutation.mutate(selectedFile)
  }

  return (
    <div id="app">
      <header className="app-header">
        <h1>My Drive</h1>
        <div className="user-bar">
          <span>
            Logged in as <strong>{user?.username}</strong>
          </span>
          <button type="button" onClick={logout}>
            Log out
          </button>
        </div>
      </header>

      <section className="toolbar">
        <div className="upload-controls">
          <input
            type="file"
            onChange={(e) => setSelectedFile(e.target.files?.[0] ?? null)}
          />
          <button
            type="button"
            onClick={handleUploadClick}
            disabled={!selectedFile || uploadMutation.isPending}
          >
            {uploadMutation.isPending ? 'Uploading…' : 'Upload'}
          </button>
        </div>

        <label className="extension-filter">
          Filter:
          <select
            value={extensionFilter ?? ''}
            onChange={(e) => setExtensionFilter(e.target.value || null)}
          >
            <option value="">All files</option>
            {extensionOptions.map((ext) => (
              <option key={ext} value={ext}>
                .{ext}
              </option>
            ))}
          </select>
        </label>
      </section>

      {actionError && <p className="error">{actionError}</p>}

      <fieldset className="column-toggles">
        <legend>Columns</legend>
        {table
          .getAllLeafColumns()
          .filter((column) => column.getCanHide())
          .map((column) => (
            <label key={column.id}>
              <input
                type="checkbox"
                checked={column.getIsVisible()}
                onChange={column.getToggleVisibilityHandler()}
              />
              {typeof column.columnDef.header === 'string'
                ? column.columnDef.header
                : column.id}
            </label>
          ))}
      </fieldset>

      {filesQuery.isLoading && <p>Loading files…</p>}
      {filesQuery.isError && (
        <p className="error">
          Failed to load files
          {filesQuery.error instanceof Error ? `: ${filesQuery.error.message}` : ''}
        </p>
      )}

      {!filesQuery.isLoading && !filesQuery.isError && (
        <table className="file-table">
          <thead>
            {table.getHeaderGroups().map((headerGroup) => (
              <tr key={headerGroup.id}>
                {headerGroup.headers.map((header) => (
                  <th key={header.id}>
                    {header.isPlaceholder
                      ? null
                      : flexRender(
                          header.column.columnDef.header,
                          header.getContext(),
                        )}
                  </th>
                ))}
              </tr>
            ))}
          </thead>
          <tbody>
            {table.getRowModel().rows.length === 0 ? (
              <tr>
                <td
                  className="empty-state"
                  colSpan={table.getVisibleLeafColumns().length}
                >
                  No files yet.
                </td>
              </tr>
            ) : (
              table.getRowModel().rows.map((row) => (
                <tr key={row.id}>
                  {row.getVisibleCells().map((cell) => (
                    <td key={cell.id}>
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </td>
                  ))}
                </tr>
              ))
            )}
          </tbody>
        </table>
      )}

      {preview && <FilePreviewModal preview={preview} onClose={closePreview} />}
    </div>
  )
}

function App() {
  const { user } = useAuth()

  if (!user) {
    return <LoginPage />
  }

  return <DriveView />
}

export default App
