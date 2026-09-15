import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  getCoreRowModel,
  useReactTable,
  flexRender,
  type SortingState,
  type VisibilityState,
} from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createColumns } from './columns'
import type { FileRecord } from './types'
import type { SortField, SortOrder } from './api/types'
import * as api from './api/client'
import { useAuth } from './auth/AuthContext'
import { LoginPage } from './auth/LoginPage'
import { FilePreviewModal, type FilePreviewState } from './FilePreviewModal'
import { SettingsModal } from './SettingsModal'
import './App.css'

function headerLabel(header: unknown, fallbackId: string): string {
  return typeof header === 'string' ? header : fallbackId
}

function sortLabel(sorted: false | 'asc' | 'desc'): string {
  if (sorted === 'asc') return 'ascending'
  if (sorted === 'desc') return 'descending'
  return 'unsorted'
}

function sortIndicator(sorted: false | 'asc' | 'desc'): string {
  if (sorted === 'asc') return '↑'
  if (sorted === 'desc') return '↓'
  return '↕'
}

// How many files GET /api/files returns per page for the main (windowed,
// infinite-scroll) table query.
const PAGE_SIZE = 50

// Used only for the extension-filter dropdown's option list, which needs to
// see every extension in play, not just the current page. The backend caps
// "limit" at 1000 (see files.go), so this is "effectively everything" for
// the small, personal-drive-sized file counts this app targets — a
// dedicated distinct-extensions endpoint would be more precise at larger
// scale, but isn't warranted here.
const ALL_FILES_LIMIT = 1000

interface UploadProgress {
  name: string
  loaded: number
  total: number
}

function DriveView() {
  const { user, logout } = useAuth()
  const queryClient = useQueryClient()

  const [columnVisibility, setColumnVisibility] = useState<VisibilityState>({})
  // Name, ascending, is the default sort (REQUIREMENTS.md §4); every other
  // column becomes sortable too by clicking its header (see the `<th>`
  // rendering below). Sorting now happens server-side — see filesQuery
  // below — since each page must already arrive in the right order for
  // infinite scroll to make sense; `sorting` only drives which sort/order
  // params get sent and which header shows the ↑/↓ indicator.
  const [sorting, setSorting] = useState<SortingState>([{ id: 'name', desc: false }])
  const [extensionFilter, setExtensionFilter] = useState<string | null>(null)
  const [selectedFiles, setSelectedFiles] = useState<File[]>([])
  // One entry per file in the mutation currently in flight, in the same
  // order — updated as each chunk finishes uploading (see uploadMutation).
  const [uploadProgress, setUploadProgress] = useState<UploadProgress[]>([])
  const [actionError, setActionError] = useState<string | null>(null)
  const [preview, setPreview] = useState<FilePreviewState | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [isDraggingOver, setIsDraggingOver] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)
  // Counts nested dragenter/dragleave pairs so the overlay doesn't flicker
  // as the drag passes over child elements — only the outermost leave (the
  // one that actually exits #app) should clear isDraggingOver.
  const dragCounter = useRef(0)

  const activeSort = sorting[0] as { id: string; desc: boolean } | undefined
  const sortField = activeSort?.id as SortField | undefined
  const sortOrder: SortOrder = activeSort?.desc ? 'desc' : 'asc'

  // The table's own data: filtered and sorted server-side, fetched one page
  // at a time. Changing extensionFilter/sortField/sortOrder changes this
  // query's key, so React Query starts a fresh paginated fetch from offset 0
  // rather than trying to re-sort/re-filter whatever pages happen to be
  // loaded already.
  const filesQuery = useInfiniteQuery({
    queryKey: ['files', { extension: extensionFilter, sort: sortField, order: sortOrder }],
    queryFn: ({ pageParam }) =>
      api.listFiles({
        ...(extensionFilter ? { extension: extensionFilter } : {}),
        ...(sortField ? { sort: sortField, order: sortOrder } : {}),
        limit: PAGE_SIZE,
        offset: pageParam,
      }),
    initialPageParam: 0,
    getNextPageParam: (lastPage, allPages) =>
      lastPage.hasMore ? allPages.length * PAGE_SIZE : undefined,
  })

  // A second, filter/sort-independent fetch used only to populate the
  // extension dropdown's option list. Using the same (filtered) query for
  // this would make the dropdown collapse to just the selected extension
  // once a filter is applied, since the backend already excludes everything
  // else.
  const allFilesQuery = useQuery({
    queryKey: ['files', 'all-extensions'],
    queryFn: () => api.listFiles({ limit: ALL_FILES_LIMIT }),
  })

  const extensionOptions = useMemo(() => {
    const set = new Set<string>()
    for (const file of allFilesQuery.data?.files ?? []) {
      if (file.extension) set.add(file.extension)
    }
    return Array.from(set).sort()
  }, [allFilesQuery.data])

  // Uploads each file in turn (sequentially, not in parallel, to keep
  // behavior predictable and not hammer the server with a burst of
  // concurrent uploads), each split into chunks by uploadFileInChunks so
  // upload progress is visible and large files never ride in a single huge
  // request. A failure on one file doesn't abort the rest; failures are
  // collected and reported together once every file has been attempted.
  const uploadMutation = useMutation({
    mutationFn: async (files: File[]) => {
      const failures: string[] = []
      setUploadProgress(files.map((f) => ({ name: f.name, loaded: 0, total: f.size })))
      for (let i = 0; i < files.length; i++) {
        const file = files[i]
        try {
          await api.uploadFileInChunks(file, (loaded, total) => {
            setUploadProgress((prev) =>
              prev.map((p, idx) => (idx === i ? { ...p, loaded, total } : p)),
            )
          })
        } catch (err) {
          failures.push(`${file.name}: ${err instanceof Error ? err.message : 'upload failed'}`)
        }
      }
      return failures
    },
    onSuccess: (failures) => {
      setSelectedFiles([])
      setUploadProgress([])
      // Reset the native input too — clearing `selectedFiles` alone leaves the
      // browser's own "chosen file(s)" caption showing the just-uploaded names.
      if (fileInputRef.current) fileInputRef.current.value = ''
      void queryClient.invalidateQueries({ queryKey: ['files'] })
      setActionError(failures.length > 0 ? `Some uploads failed — ${failures.join('; ')}` : null)
    },
    onError: (err: unknown) => {
      setUploadProgress([])
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
        onDownload: (id) => void handleDownload(id),
        onDelete: handleDelete,
        onPreview: (file) => void handlePreview(file),
      }),
    [handleDownload, handleDelete, handlePreview],
  )

  const data: FileRecord[] = useMemo(
    () => filesQuery.data?.pages.flatMap((page) => page.files) ?? [],
    [filesQuery.data],
  )

  const table = useReactTable({
    data,
    columns,
    state: {
      columnVisibility,
      sorting,
    },
    onColumnVisibilityChange: setColumnVisibility,
    onSortingChange: setSorting,
    // Sorting is server-side (see filesQuery above): rows already arrive in
    // the right order, so there's no client-side row model to compute here.
    manualSorting: true,
    getCoreRowModel: getCoreRowModel(),
  })

  const handleUploadClick = () => {
    if (selectedFiles.length === 0) return
    uploadMutation.mutate(selectedFiles)
  }

  // Dropping files anywhere on the app uploads them immediately — it's a
  // separate entry point from the file input/"Upload" button above, not
  // something that populates selectedFiles first.
  const handleDragEnter = (e: React.DragEvent) => {
    e.preventDefault()
    if (!e.dataTransfer.types.includes('Files')) return
    dragCounter.current += 1
    setIsDraggingOver(true)
  }

  const handleDragOver = (e: React.DragEvent) => {
    // Merely required so onDrop fires at all — browsers reject drops on
    // elements that don't cancel dragover.
    e.preventDefault()
  }

  const handleDragLeave = (e: React.DragEvent) => {
    e.preventDefault()
    dragCounter.current = Math.max(0, dragCounter.current - 1)
    if (dragCounter.current === 0) setIsDraggingOver(false)
  }

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault()
    dragCounter.current = 0
    setIsDraggingOver(false)
    const files = Array.from(e.dataTransfer.files)
    if (files.length === 0) return
    uploadMutation.mutate(files)
  }

  const { rows } = table.getRowModel()
  const visibleLeafColumnCount = table.getVisibleLeafColumns().length

  // Windowed rendering: the scrollable region (.file-table-viewport) only
  // ever holds a small, roughly constant number of <tr> elements — enough to
  // cover the visible viewport plus a little overscan — no matter how many
  // files are in `rows`. Scrolling re-targets each element at a different
  // row's data instead of growing the DOM, so there's no "loading more rows"
  // moment and no page-level scrollbar (see index.css/App.css).
  const tableContainerRef = useRef<HTMLDivElement>(null)
  const rowVirtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => tableContainerRef.current,
    estimateSize: () => 41,
    overscan: 8,
    measureElement: (el) => el.getBoundingClientRect().height,
  })
  const virtualRows = rowVirtualizer.getVirtualItems()
  const paddingTop = virtualRows.length > 0 ? virtualRows[0].start : 0
  const paddingBottom =
    virtualRows.length > 0 ? rowVirtualizer.getTotalSize() - virtualRows[virtualRows.length - 1].end : 0

  const { hasNextPage, isFetchingNextPage, fetchNextPage } = filesQuery

  // The actual "load the next page" trigger for server-paged infinite
  // scroll: once the virtualizer has rendered up to the last currently-
  // loaded row, fetch the next page so scrolling further has more data
  // ready — same idea as the mockup's "loading more…" row at the bottom.
  useEffect(() => {
    const lastVirtualRow = virtualRows[virtualRows.length - 1]
    if (!lastVirtualRow) return
    if (lastVirtualRow.index < rows.length - 1) return
    if (!hasNextPage || isFetchingNextPage) return
    void fetchNextPage()
  }, [virtualRows, rows.length, hasNextPage, isFetchingNextPage, fetchNextPage])

  return (
    <div
      id="app"
      onDragEnter={handleDragEnter}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {isDraggingOver && (
        <div className="drag-overlay" aria-hidden="true">
          Drop files to upload
        </div>
      )}
      <header className="app-header">
        <h1>My Drive</h1>
        <div className="user-bar">
          <span>
            Logged in as <strong>{user?.username}</strong>
          </span>
          <button type="button" onClick={() => setSettingsOpen(true)}>
            Settings
          </button>
          <button type="button" onClick={logout}>
            Log out
          </button>
        </div>
      </header>

      <section className="toolbar">
        <div className="upload-controls">
          <input
            ref={fileInputRef}
            type="file"
            multiple
            onChange={(e) => setSelectedFiles(Array.from(e.target.files ?? []))}
          />
          <button
            type="button"
            onClick={handleUploadClick}
            disabled={selectedFiles.length === 0 || uploadMutation.isPending}
          >
            {uploadMutation.isPending
              ? 'Uploading…'
              : selectedFiles.length > 1
                ? `Upload ${selectedFiles.length} files`
                : 'Upload'}
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

      {uploadProgress.length > 0 && (
        <ul className="upload-progress">
          {uploadProgress.map((p, i) => {
            const pct = p.total > 0 ? Math.round((p.loaded / p.total) * 100) : 100
            return (
              <li key={`${p.name}-${i}`}>
                <span className="upload-progress-name">{p.name}</span>
                <progress className="upload-progress-bar" value={p.loaded} max={Math.max(p.total, 1)} />
                <span className="upload-progress-pct">{pct}%</span>
              </li>
            )
          })}
        </ul>
      )}

      {actionError && <p className="error">{actionError}</p>}

      {filesQuery.isLoading && <p>Loading files…</p>}
      {filesQuery.isError && (
        <p className="error">
          Failed to load files
          {filesQuery.error instanceof Error ? `: ${filesQuery.error.message}` : ''}
        </p>
      )}

      {!filesQuery.isLoading && !filesQuery.isError && (
        <div className="file-table-viewport" ref={tableContainerRef}>
          <table className="file-table">
            <thead>
              {table.getHeaderGroups().map((headerGroup) => (
                <tr key={headerGroup.id}>
                  {headerGroup.headers.map((header) => (
                    <th key={header.id}>
                      {header.isPlaceholder ? null : header.column.getCanSort() ? (
                        <button
                          type="button"
                          className="sort-toggle"
                          onClick={header.column.getToggleSortingHandler()}
                          aria-label={`Sort by ${headerLabel(header.column.columnDef.header, header.column.id)} (currently ${sortLabel(header.column.getIsSorted())})`}
                        >
                          {flexRender(header.column.columnDef.header, header.getContext())}{' '}
                          {sortIndicator(header.column.getIsSorted())}
                        </button>
                      ) : (
                        flexRender(header.column.columnDef.header, header.getContext())
                      )}
                    </th>
                  ))}
                </tr>
              ))}
            </thead>
            <tbody>
              {rows.length === 0 ? (
                <tr>
                  <td className="empty-state" colSpan={visibleLeafColumnCount}>
                    No files yet.
                  </td>
                </tr>
              ) : (
                <>
                  {paddingTop > 0 && (
                    <tr aria-hidden="true">
                      <td
                        colSpan={visibleLeafColumnCount}
                        style={{ height: paddingTop, padding: 0, border: 0 }}
                      />
                    </tr>
                  )}
                  {virtualRows.map((virtualRow) => {
                    const row = rows[virtualRow.index]
                    return (
                      <tr
                        key={row.id}
                        data-index={virtualRow.index}
                        ref={rowVirtualizer.measureElement}
                      >
                        {row.getVisibleCells().map((cell) => (
                          <td key={cell.id}>
                            {flexRender(cell.column.columnDef.cell, cell.getContext())}
                          </td>
                        ))}
                      </tr>
                    )
                  })}
                  {paddingBottom > 0 && (
                    <tr aria-hidden="true">
                      <td
                        colSpan={visibleLeafColumnCount}
                        style={{ height: paddingBottom, padding: 0, border: 0 }}
                      />
                    </tr>
                  )}
                  {isFetchingNextPage && (
                    <tr>
                      <td className="loading-more" colSpan={visibleLeafColumnCount}>
                        Loading more…
                      </td>
                    </tr>
                  )}
                </>
              )}
            </tbody>
          </table>
        </div>
      )}

      {preview && <FilePreviewModal preview={preview} onClose={closePreview} />}
      {settingsOpen && (
        <SettingsModal columns={table.getAllLeafColumns()} onClose={() => setSettingsOpen(false)} />
      )}
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
