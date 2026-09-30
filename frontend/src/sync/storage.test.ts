import { describe, expect, it } from 'vitest'
import {
  createMemoryKVStore,
  emptySyncState,
  loadDirectoryHandle,
  loadSyncState,
  saveSyncState,
  selectDirectory,
  type SyncState,
} from './storage'

/** Compares by name, matching how a real FileSystemHandle.isSameEntry()
 * would behave for two handles pointing at the same vs. a different path. */
function fakeDirectoryHandle(name: string): FileSystemDirectoryHandle {
  return {
    kind: 'directory',
    name,
    isSameEntry: async (other: unknown) => (other as { name?: string } | undefined)?.name === name,
  } as unknown as FileSystemDirectoryHandle
}

describe('sync/storage', () => {
  it('round-trips sync state through the KV store', async () => {
    const store = createMemoryKVStore()
    const state: SyncState = {
      lastSyncedVersion: 7,
      entries: { 'a.png': { remoteID: 1, size: 100, mtime: 12345, sha256: 'abc' } },
    }
    await saveSyncState(store, state)

    const loaded = await loadSyncState(store)
    expect(loaded).toEqual(state)
  })

  it('loadSyncState returns an empty state when nothing has been saved', async () => {
    const store = createMemoryKVStore()
    expect(await loadSyncState(store)).toEqual(emptySyncState())
  })

  it('selectDirectory resets sync state when switching to a different folder', async () => {
    const store = createMemoryKVStore()
    const oldHandle = fakeDirectoryHandle('old-folder')
    await saveSyncState(store, {
      lastSyncedVersion: 42,
      entries: { 'known.txt': { remoteID: 99, size: 5, mtime: 0, sha256: 'irrelevant' } },
    })
    await store.set('directoryHandle', oldHandle)

    // A different name -- isSameEntry reports false, a genuinely different folder.
    const newHandle = fakeDirectoryHandle('new-folder')

    const result = await selectDirectory(store, newHandle)

    expect(result).toEqual(emptySyncState())
    expect(await loadSyncState(store)).toEqual(emptySyncState())
    expect(await loadDirectoryHandle(store)).toBe(newHandle)
  })

  it('selectDirectory preserves sync state when re-selecting the same folder', async () => {
    const store = createMemoryKVStore()
    const existingState: SyncState = {
      lastSyncedVersion: 42,
      entries: { 'known.txt': { remoteID: 99, size: 5, mtime: 0, sha256: 'irrelevant' } },
    }
    await saveSyncState(store, existingState)

    const oldHandle = fakeDirectoryHandle('same-folder')
    await store.set('directoryHandle', oldHandle)

    // Same name -- isSameEntry reports true against the handle stored above.
    const reselectedHandle = fakeDirectoryHandle('same-folder')

    const result = await selectDirectory(store, reselectedHandle)

    expect(result).toEqual(existingState)
    expect(await loadDirectoryHandle(store)).toBe(reselectedHandle)
  })

  it('selectDirectory treats no previously-stored handle as a different folder', async () => {
    const store = createMemoryKVStore()
    const handle = fakeDirectoryHandle('first-folder')

    const result = await selectDirectory(store, handle)

    expect(result).toEqual(emptySyncState())
  })
})
