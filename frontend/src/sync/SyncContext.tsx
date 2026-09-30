import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useQueryClient } from '@tanstack/react-query'
import * as apiClient from '../api/client'
import {
  reconcile,
  resolveKeepBoth as resolveKeepBothImpl,
  resolveKeepLocal as resolveKeepLocalImpl,
  resolveKeepRemote as resolveKeepRemoteImpl,
  type SyncConflict,
} from './reconcile'
import {
  IndexedDBStore,
  emptySyncState,
  loadDirectoryHandle,
  loadSyncState,
  saveSyncState,
  selectDirectory,
  type SyncState,
} from './storage'

export type { SyncConflict } from './reconcile'

export interface SyncContextValue {
  /** False on non-Chromium browsers, which don't implement
   * window.showDirectoryPicker() — see SyncModal.tsx's fallback UI. */
  isSupported: boolean
  folderName: string | null
  isSyncing: boolean
  conflicts: SyncConflict[]
  log: string[]
  /** True once a sync attempt has found the persisted folder handle's
   * permission isn't (or is no longer) granted. Surfaced as its own state
   * distinct from a generic error, since the fix is "click Sync now again"
   * (a fresh user gesture), not a bug to report. */
  permissionNeeded: boolean
  lastError: string | null
  chooseFolder: () => Promise<void>
  syncNow: () => Promise<void>
  resolveKeepLocal: (conflict: SyncConflict) => Promise<void>
  resolveKeepRemote: (conflict: SyncConflict) => Promise<void>
  resolveKeepBoth: (conflict: SyncConflict) => Promise<void>
}

const SyncContext = createContext<SyncContextValue | null>(null)

const MAX_LOG_LINES = 50

const isSupported = typeof window !== 'undefined' && 'showDirectoryPicker' in window

export function SyncProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const storeRef = useRef(new IndexedDBStore())

  const [directoryHandle, setDirectoryHandle] = useState<FileSystemDirectoryHandle | null>(null)
  const [folderName, setFolderName] = useState<string | null>(null)
  const [syncState, setSyncState] = useState<SyncState>(emptySyncState())
  const [conflicts, setConflicts] = useState<SyncConflict[]>([])
  const [log, setLog] = useState<string[]>([])
  const [isSyncing, setIsSyncing] = useState(false)
  const [permissionNeeded, setPermissionNeeded] = useState(false)
  const [lastError, setLastError] = useState<string | null>(null)

  const appendLog = useCallback((lines: string[]) => {
    if (lines.length === 0) return
    setLog((prev) => {
      const next = [...prev, ...lines]
      return next.length > MAX_LOG_LINES ? next.slice(next.length - MAX_LOG_LINES) : next
    })
  }, [])

  // Restore a previously-chosen folder + its sync bookkeeping on load.
  // queryPermission() never needs a user gesture, so it's safe to call here
  // just to reflect the current permission state in the UI -- actually
  // *requesting* permission only ever happens inside syncNow(), which is
  // only ever called from a button click.
  useEffect(() => {
    if (!isSupported) return
    void (async () => {
      const store = storeRef.current
      const [handle, state] = await Promise.all([loadDirectoryHandle(store), loadSyncState(store)])
      if (!handle) return
      setDirectoryHandle(handle)
      setFolderName(handle.name)
      setSyncState(state)
      const perm = await handle.queryPermission({ mode: 'readwrite' })
      setPermissionNeeded(perm !== 'granted')
    })()
  }, [])

  const chooseFolder = useCallback(async () => {
    if (!isSupported) return
    try {
      const handle = await window.showDirectoryPicker({ mode: 'readwrite' })
      const nextState = await selectDirectory(storeRef.current, handle)
      setDirectoryHandle(handle)
      setFolderName(handle.name)
      setSyncState(nextState)
      setConflicts([])
      setPermissionNeeded(false)
      setLastError(null)
    } catch (err) {
      // The user dismissed the picker -- not a real failure, say nothing.
      if (err instanceof DOMException && err.name === 'AbortError') return
      setLastError(err instanceof Error ? err.message : 'Failed to choose a folder')
    }
  }, [])

  const syncNow = useCallback(async () => {
    if (!directoryHandle || isSyncing) return
    setIsSyncing(true)
    try {
      // Called from this click handler specifically because
      // requestPermission() must run inside a user gesture -- see the
      // module header and SyncModal.tsx's "Sync now" button.
      const current = await directoryHandle.queryPermission({ mode: 'readwrite' })
      const granted =
        current === 'granted'
          ? true
          : (await directoryHandle.requestPermission({ mode: 'readwrite' })) === 'granted'
      if (!granted) {
        setPermissionNeeded(true)
        appendLog(['Permission needed — click Sync now again'])
        return
      }
      setPermissionNeeded(false)

      const result = await reconcile(directoryHandle, syncState, conflicts, apiClient)
      const nextState: SyncState = { lastSyncedVersion: result.lastSyncedVersion, entries: result.entries }
      setSyncState(nextState)
      setConflicts(result.conflicts)
      appendLog(result.log)
      await saveSyncState(storeRef.current, nextState)
      setLastError(null)
      // The reconcile pass may have uploaded/downloaded/deleted files
      // behind TanStack Query's back -- without this, the visible file
      // table wouldn't reflect a successful sync until an unrelated
      // refetch (same pattern App.tsx's upload/delete mutations use).
      void queryClient.invalidateQueries({ queryKey: ['files'] })
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Sync failed'
      setLastError(message)
      appendLog([`Sync error: ${message}`])
    } finally {
      setIsSyncing(false)
    }
  }, [directoryHandle, isSyncing, syncState, conflicts, queryClient, appendLog])

  const resolveKeepLocal = useCallback(
    async (conflict: SyncConflict) => {
      if (!directoryHandle) return
      try {
        const newEntries = await resolveKeepLocalImpl(directoryHandle, conflict, syncState.entries, apiClient)
        const nextState: SyncState = { ...syncState, entries: newEntries }
        setSyncState(nextState)
        await saveSyncState(storeRef.current, nextState)
        setConflicts((prev) => prev.filter((c) => c.name !== conflict.name))
        appendLog([`Kept local version of ${conflict.name}`])
        void queryClient.invalidateQueries({ queryKey: ['files'] })
      } catch (err) {
        appendLog([
          `Failed to keep local version of ${conflict.name}: ${err instanceof Error ? err.message : 'failed'}`,
        ])
      }
    },
    [directoryHandle, syncState, queryClient, appendLog],
  )

  const resolveKeepRemote = useCallback(
    async (conflict: SyncConflict) => {
      if (!directoryHandle) return
      try {
        const newEntries = await resolveKeepRemoteImpl(directoryHandle, conflict, syncState.entries, apiClient)
        const nextState: SyncState = { ...syncState, entries: newEntries }
        setSyncState(nextState)
        await saveSyncState(storeRef.current, nextState)
        setConflicts((prev) => prev.filter((c) => c.name !== conflict.name))
        appendLog([`Kept remote version of ${conflict.name}`])
        void queryClient.invalidateQueries({ queryKey: ['files'] })
      } catch (err) {
        appendLog([
          `Failed to keep remote version of ${conflict.name}: ${err instanceof Error ? err.message : 'failed'}`,
        ])
      }
    },
    [directoryHandle, syncState, queryClient, appendLog],
  )

  const resolveKeepBoth = useCallback(
    async (conflict: SyncConflict) => {
      if (!directoryHandle) return
      try {
        const newEntries = await resolveKeepBothImpl(directoryHandle, conflict, syncState.entries, apiClient)
        const nextState: SyncState = { ...syncState, entries: newEntries }
        setSyncState(nextState)
        await saveSyncState(storeRef.current, nextState)
        setConflicts((prev) => prev.filter((c) => c.name !== conflict.name))
        appendLog([`Kept both versions of ${conflict.name}`])
        void queryClient.invalidateQueries({ queryKey: ['files'] })
      } catch (err) {
        appendLog([
          `Failed to keep both versions of ${conflict.name}: ${err instanceof Error ? err.message : 'failed'}`,
        ])
      }
    },
    [directoryHandle, syncState, queryClient, appendLog],
  )

  const value = useMemo<SyncContextValue>(
    () => ({
      isSupported,
      folderName,
      isSyncing,
      conflicts,
      log,
      permissionNeeded,
      lastError,
      chooseFolder,
      syncNow,
      resolveKeepLocal,
      resolveKeepRemote,
      resolveKeepBoth,
    }),
    [
      folderName,
      isSyncing,
      conflicts,
      log,
      permissionNeeded,
      lastError,
      chooseFolder,
      syncNow,
      resolveKeepLocal,
      resolveKeepRemote,
      resolveKeepBoth,
    ],
  )

  return <SyncContext.Provider value={value}>{children}</SyncContext.Provider>
}

export function useSync(): SyncContextValue {
  const ctx = useContext(SyncContext)
  if (!ctx) {
    throw new Error('useSync must be used within a SyncProvider')
  }
  return ctx
}
