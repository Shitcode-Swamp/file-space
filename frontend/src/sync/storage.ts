/**
 * Persisted sync state (REQUIREMENTS.md §6.3's "sync cursor" per client) —
 * the chosen folder's directory handle, the last-synced version, and the
 * known per-file entries, mirroring desktop's SyncState/SyncedFileEntry
 * (Manifest.swift). Kept behind a small injectable KV-store interface so
 * tests substitute an in-memory Map instead of real IndexedDB (jsdom has
 * neither IndexedDB nor the File System Access API) — the same seam
 * desktop's APIClientProtocol plays for SyncViewModel.
 */

export interface KVStore {
  get<T>(key: string): Promise<T | undefined>
  set<T>(key: string, value: T): Promise<void>
  delete(key: string): Promise<void>
}

const DB_NAME = 'filespace-sync'
const DB_VERSION = 1
const STORE_NAME = 'kv'

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, DB_VERSION)
    request.onupgradeneeded = () => {
      request.result.createObjectStore(STORE_NAME)
    }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('failed to open IndexedDB'))
  })
}

/** Real, browser-backed implementation of KVStore. FileSystemDirectoryHandle
 * is structured-cloneable, so it can be stored as a value here directly,
 * without any serialization step — that's what lets the chosen folder
 * survive a page reload. */
export class IndexedDBStore implements KVStore {
  private dbPromise: Promise<IDBDatabase> | null = null

  private db(): Promise<IDBDatabase> {
    if (!this.dbPromise) this.dbPromise = openDB()
    return this.dbPromise
  }

  async get<T>(key: string): Promise<T | undefined> {
    const db = await this.db()
    return new Promise((resolve, reject) => {
      const tx = db.transaction(STORE_NAME, 'readonly')
      const request = tx.objectStore(STORE_NAME).get(key)
      request.onsuccess = () => resolve(request.result as T | undefined)
      request.onerror = () => reject(request.error ?? new Error(`failed to read ${key}`))
    })
  }

  async set<T>(key: string, value: T): Promise<void> {
    const db = await this.db()
    return new Promise((resolve, reject) => {
      const tx = db.transaction(STORE_NAME, 'readwrite')
      tx.objectStore(STORE_NAME).put(value, key)
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error ?? new Error(`failed to write ${key}`))
    })
  }

  async delete(key: string): Promise<void> {
    const db = await this.db()
    return new Promise((resolve, reject) => {
      const tx = db.transaction(STORE_NAME, 'readwrite')
      tx.objectStore(STORE_NAME).delete(key)
      tx.oncomplete = () => resolve()
      tx.onerror = () => reject(tx.error ?? new Error(`failed to delete ${key}`))
    })
  }
}

/** In-memory stand-in — no persistence across reloads, just enough surface
 * for tests to exercise state read/write without real IndexedDB. */
export function createMemoryKVStore(): KVStore {
  const map = new Map<string, unknown>()
  return {
    get: <T>(key: string) => Promise.resolve(map.get(key) as T | undefined),
    set: <T>(key: string, value: T) => {
      map.set(key, value)
      return Promise.resolve()
    },
    delete: (key: string) => {
      map.delete(key)
      return Promise.resolve()
    },
  }
}

/** Mirrors desktop's SyncedFileEntry (Manifest.swift). */
export interface SyncedFileEntry {
  remoteID: number
  size: number
  mtime: number
  sha256: string
}

/** Mirrors desktop's SyncState, minus folderPath — the directory handle
 * itself (stored separately, see DIRECTORY_HANDLE_KEY below) is what
 * identifies the chosen folder on the web, there's no path string. */
export interface SyncState {
  lastSyncedVersion: number
  entries: Record<string, SyncedFileEntry>
}

export function emptySyncState(): SyncState {
  return { lastSyncedVersion: 0, entries: {} }
}

const SYNC_STATE_KEY = 'syncState'
const DIRECTORY_HANDLE_KEY = 'directoryHandle'

export async function loadSyncState(store: KVStore): Promise<SyncState> {
  return (await store.get<SyncState>(SYNC_STATE_KEY)) ?? emptySyncState()
}

export async function saveSyncState(store: KVStore, state: SyncState): Promise<void> {
  await store.set(SYNC_STATE_KEY, state)
}

export async function loadDirectoryHandle(
  store: KVStore,
): Promise<FileSystemDirectoryHandle | undefined> {
  return store.get<FileSystemDirectoryHandle>(DIRECTORY_HANDLE_KEY)
}

export async function saveDirectoryHandle(
  store: KVStore,
  handle: FileSystemDirectoryHandle,
): Promise<void> {
  await store.set(DIRECTORY_HANDLE_KEY, handle)
}

/**
 * Selecting a *different* folder than the one already persisted must reset
 * lastSyncedVersion/entries — the stored state describes one specific
 * folder's bookkeeping, and carrying it over to a new (possibly empty) one
 * makes reconcile() think the new folder already has whatever files were
 * known-synced from the old one. At best that silently blocks their
 * download; at worst the "missing locally" pass reads their absence from
 * the new folder as a local deletion and deletes them remotely. Mirrors
 * desktop's SyncViewModel.selectFolder(path:) exactly. Re-selecting the
 * same folder (detected via FileSystemHandle.isSameEntry) intentionally
 * keeps its progress.
 */
export async function selectDirectory(
  store: KVStore,
  handle: FileSystemDirectoryHandle,
): Promise<SyncState> {
  const previous = await loadDirectoryHandle(store)
  const isSameFolder = previous ? await previous.isSameEntry(handle) : false

  await saveDirectoryHandle(store, handle)

  if (isSameFolder) {
    return loadSyncState(store)
  }
  const fresh = emptySyncState()
  await saveSyncState(store, fresh)
  return fresh
}
