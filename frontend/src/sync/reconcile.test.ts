import { describe, expect, it } from 'vitest'
import {
  conflictCopyName,
  reconcile,
  resolveKeepBoth,
  resolveKeepLocal,
  resolveKeepRemote,
  type ReconcileApi,
  type SyncConflict,
} from './reconcile'
import { emptySyncState, type SyncedFileEntry, type SyncState } from './storage'
import { sha256Hex } from './hash'
import type { ApiFileRecord, ListFilesResult, SyncAction, SyncDiffResponse, SyncManifestEntry } from '../api/types'

// ---- Fake directory handle: an in-memory folder driving folderScanner.ts
// and reconcile.ts's read/write/remove calls without a real File System
// Access API (jsdom has none). Mirrors desktop's SyncViewModelTests.swift
// use of real temp directories, adapted to the web's handle-based API.

class FakeFileHandle {
  private dir: FakeDirectory
  name: string

  constructor(dir: FakeDirectory, name: string) {
    this.dir = dir
    this.name = name
  }

  readonly kind = 'file' as const

  async getFile(): Promise<File> {
    const record = this.dir.files.get(this.name)
    if (!record) throw new DOMException('not found', 'NotFoundError')
    return new File([record.content], this.name, { lastModified: record.lastModified })
  }

  async createWritable() {
    let buffer: Blob = new Blob()
    return {
      write: async (data: Blob | BufferSource | string) => {
        buffer = data instanceof Blob ? data : new Blob([data as BlobPart])
      },
      close: async () => {
        this.dir.files.set(this.name, { content: await buffer.arrayBuffer(), lastModified: Date.now() })
      },
    }
  }
}

class FakeDirectory {
  files = new Map<string, { content: ArrayBuffer | string; lastModified: number }>()
  readonly kind = 'directory' as const
  name = 'root'

  constructor(seed: Record<string, { content: string; lastModified?: number }> = {}) {
    for (const [name, { content, lastModified }] of Object.entries(seed)) {
      this.files.set(name, { content, lastModified: lastModified ?? Date.now() })
    }
  }

  async *values() {
    for (const name of this.files.keys()) {
      yield new FakeFileHandle(this, name)
    }
  }

  async getFileHandle(name: string, options?: { create?: boolean }): Promise<FakeFileHandle> {
    if (!this.files.has(name)) {
      if (options?.create) {
        this.files.set(name, { content: '', lastModified: Date.now() })
      } else {
        throw new DOMException('not found', 'NotFoundError')
      }
    }
    return new FakeFileHandle(this, name)
  }

  async removeEntry(name: string): Promise<void> {
    if (!this.files.delete(name)) {
      throw new DOMException('not found', 'NotFoundError')
    }
  }

  async textOf(name: string): Promise<string | undefined> {
    const record = this.files.get(name)
    if (!record) return undefined
    return new File([record.content], name).text()
  }

  asHandle(): FileSystemDirectoryHandle {
    return this as unknown as FileSystemDirectoryHandle
  }
}

// ---- Fake API client: mirrors desktop's FakeAPIClient (SyncViewModelTests.swift).

class FakeSyncApi implements ReconcileApi {
  remoteFiles: ApiFileRecord[] = []
  diffActions: SyncAction[] = []
  diffNewVersion = 0
  downloadContentById = new Map<number, string>()
  uploadedContentByName = new Map<string, string>()
  uploadCallCount = 0
  deletedIds: number[] = []
  lastManifestSent: SyncManifestEntry[] | undefined
  private nextUploadId = 1000

  async listFiles(): Promise<ListFilesResult> {
    return { files: this.remoteFiles, hasMore: false }
  }

  async syncDiff(_lastSyncedVersion: number, manifest: SyncManifestEntry[]): Promise<SyncDiffResponse> {
    this.lastManifestSent = manifest
    return { newVersion: this.diffNewVersion, actions: this.diffActions }
  }

  async downloadFile(id: number): Promise<{ blob: Blob; filename: string | null }> {
    const content = this.downloadContentById.get(id) ?? ''
    const file = this.remoteFiles.find((f) => f.id === id)
    return { blob: new Blob([content]), filename: file?.name ?? null }
  }

  async uploadFileInChunks(file: File): Promise<ApiFileRecord> {
    this.uploadCallCount += 1
    this.uploadedContentByName.set(file.name, await file.text())
    const id = this.nextUploadId++
    return {
      id,
      name: file.name,
      extension: file.name.includes('.') ? file.name.split('.').pop()! : '',
      size: file.size,
      createdAt: new Date().toISOString(),
      modifiedAt: new Date().toISOString(),
      uploadedBy: 'tester',
      editedBy: 'tester',
    }
  }

  async deleteFile(id: number): Promise<void> {
    this.deletedIds.push(id)
  }
}

function makeState(entries: Record<string, SyncedFileEntry> = {}, lastSyncedVersion = 0): SyncState {
  return { lastSyncedVersion, entries }
}

describe('sync/reconcile', () => {
  it('uploads a new local file that is not yet known or present remotely', async () => {
    const dir = new FakeDirectory({ 'new.txt': { content: 'brand new contents' } })
    const api = new FakeSyncApi()

    const result = await reconcile(dir.asHandle(), emptySyncState(), [], api)

    expect(api.uploadCallCount).toBe(1)
    expect(api.uploadedContentByName.get('new.txt')).toBe('brand new contents')
    expect(api.deletedIds).toEqual([])
    expect(result.entries['new.txt']).toBeDefined()
  })

  it('applies a download action and writes the file with the correct bytes, without deleting it back off the server', async () => {
    const dir = new FakeDirectory()
    const api = new FakeSyncApi()
    api.remoteFiles = [
      {
        id: 7,
        name: 'remote.txt',
        extension: 'txt',
        size: 11,
        createdAt: '',
        modifiedAt: '',
        uploadedBy: 'alice',
        editedBy: 'alice',
      },
    ]
    api.diffActions = [{ name: 'remote.txt', action: 'download', remoteVersion: 1 }]
    api.diffNewVersion = 1
    api.downloadContentById.set(7, 'hello world')

    const result = await reconcile(dir.asHandle(), emptySyncState(), [], api)

    expect(await dir.textOf('remote.txt')).toBe('hello world')
    expect(api.uploadCallCount).toBe(0)
    // Regression: a file downloaded this cycle must not also be read as
    // "missing locally, so delete it remotely" by the same cycle's pass.
    expect(api.deletedIds).toEqual([])
    expect(result.entries['remote.txt']?.remoteID).toBe(7)
  })

  it('applies a delete_local action by removing the file from the folder', async () => {
    const dir = new FakeDirectory({ 'gone.txt': { content: 'x' } })
    const api = new FakeSyncApi()
    api.diffActions = [{ name: 'gone.txt', action: 'delete_local', remoteVersion: 2 }]
    api.diffNewVersion = 2
    const state = makeState({ 'gone.txt': { remoteID: 77, size: 1, mtime: 0, sha256: 'x' } }, 1)

    const result = await reconcile(dir.asHandle(), state, [], api)

    expect(dir.files.has('gone.txt')).toBe(false)
    expect(result.entries['gone.txt']).toBeUndefined()
  })

  it('re-uploads a locally changed, already-known file (delete remote then upload)', async () => {
    const dir = new FakeDirectory({ 'a.txt': { content: 'new content' } })
    const api = new FakeSyncApi()
    const state = makeState({ 'a.txt': { remoteID: 55, size: 3, mtime: 0, sha256: 'old-hash' } })

    const result = await reconcile(dir.asHandle(), state, [], api)

    expect(api.deletedIds).toEqual([55])
    expect(api.uploadCallCount).toBe(1)
    expect(result.entries['a.txt']?.remoteID).not.toBe(55)
  })

  it('deletes the remote copy of a file removed from the local folder', async () => {
    const dir = new FakeDirectory()
    const api = new FakeSyncApi()
    const state = makeState({ 'removed.txt': { remoteID: 42, size: 1, mtime: 0, sha256: 'x' } })

    const result = await reconcile(dir.asHandle(), state, [], api)

    expect(api.deletedIds).toEqual([42])
    expect(result.entries['removed.txt']).toBeUndefined()
  })

  it('sends the local manifest with name/size/sha256 on every syncDiff call', async () => {
    const dir = new FakeDirectory({ 'a.txt': { content: 'manifest me' } })
    const api = new FakeSyncApi()

    await reconcile(dir.asHandle(), emptySyncState(), [], api)

    const entry = api.lastManifestSent?.find((m) => m.name === 'a.txt')
    const expectedHash = await sha256Hex(new TextEncoder().encode('manifest me').buffer as ArrayBuffer)
    expect(entry?.size).toBe(new TextEncoder().encode('manifest me').length)
    expect(entry?.sha256).toBe(expectedHash)
  })

  it('records a conflict action and excludes it from auto-upload/auto-delete', async () => {
    const dir = new FakeDirectory({ 'shared.txt': { content: 'locally edited content' } })
    const api = new FakeSyncApi()
    api.remoteFiles = [
      {
        id: 42,
        name: 'shared.txt',
        extension: 'txt',
        size: 99,
        createdAt: '',
        modifiedAt: '',
        uploadedBy: 'bob',
        editedBy: 'bob',
      },
    ]
    api.diffActions = [
      {
        name: 'shared.txt',
        action: 'conflict',
        remoteVersion: 5,
        conflict: { remoteSize: 99, remoteModifiedAt: '2026-09-11T00:00:00Z', remoteEditedBy: 'bob' },
      },
    ]
    api.diffNewVersion = 5
    const state = makeState({ 'shared.txt': { remoteID: 42, size: 0, mtime: 0, sha256: 'old-hash' } })

    const result = await reconcile(dir.asHandle(), state, [], api)

    expect(api.uploadCallCount).toBe(0)
    expect(api.deletedIds).toEqual([])
    expect(result.conflicts).toHaveLength(1)
    expect(result.conflicts[0]).toMatchObject({
      name: 'shared.txt',
      remoteID: 42,
      remoteSize: 99,
      remoteEditedBy: 'bob',
    })
  })

  it('a file downloaded this cycle is not treated as removed-locally, even alongside a genuine local removal', async () => {
    // "goneLocally.txt" is deliberately absent from the folder -- it was
    // really removed since the last sync.
    const dir = new FakeDirectory()
    const api = new FakeSyncApi()
    api.remoteFiles = [
      {
        id: 7,
        name: 'remote.txt',
        extension: 'txt',
        size: 11,
        createdAt: '',
        modifiedAt: '',
        uploadedBy: 'alice',
        editedBy: 'alice',
      },
    ]
    api.diffActions = [{ name: 'remote.txt', action: 'download', remoteVersion: 1 }]
    api.diffNewVersion = 1
    api.downloadContentById.set(7, 'hello world')
    const state = makeState({ 'goneLocally.txt': { remoteID: 55, size: 3, mtime: 0, sha256: 'old' } })

    const result = await reconcile(dir.asHandle(), state, [], api)

    expect(dir.files.has('remote.txt')).toBe(true)
    // Only the genuinely-removed file is deleted remotely.
    expect(api.deletedIds).toEqual([55])
    expect(result.entries['remote.txt']).toBeDefined()
  })

  it('a file the server says to delete_local is not re-uploaded even though it is still present on disk', async () => {
    // "x.txt" is still physically present locally at scan time -- the
    // delete_local action removes it during this same cycle.
    const dir = new FakeDirectory({ 'x.txt': { content: 'still here at scan time' } })
    const api = new FakeSyncApi()
    api.diffActions = [{ name: 'x.txt', action: 'delete_local', remoteVersion: 9 }]
    api.diffNewVersion = 9
    const state = makeState({ 'x.txt': { remoteID: 33, size: 1, mtime: 0, sha256: 'old' } }, 8)

    const result = await reconcile(dir.asHandle(), state, [], api)

    expect(api.uploadCallCount).toBe(0)
    expect(dir.files.has('x.txt')).toBe(false)
    expect(result.entries['x.txt']).toBeUndefined()
  })

  it('an unresolved conflict from a previous cycle keeps blocking the auto-upload/auto-delete pass', async () => {
    const dir = new FakeDirectory({ 'still-conflicted.txt': { content: 'edited again' } })
    const api = new FakeSyncApi()
    const existingConflict: SyncConflict = {
      name: 'still-conflicted.txt',
      remoteID: 11,
      localSize: 5,
      localMtime: 0,
      remoteSize: 6,
      remoteModifiedAt: '',
      remoteEditedBy: 'bob',
    }
    const state = makeState({ 'still-conflicted.txt': { remoteID: 11, size: 5, mtime: 0, sha256: 'old' } })

    const result = await reconcile(dir.asHandle(), state, [existingConflict], api)

    expect(api.uploadCallCount).toBe(0)
    expect(api.deletedIds).toEqual([])
    expect(result.conflicts).toEqual([existingConflict])
  })
})

describe('sync/reconcile conflict resolution', () => {
  it('resolveKeepLocal deletes the remote copy and re-uploads the local file', async () => {
    const dir = new FakeDirectory({ 'shared.txt': { content: 'keep the local edit' } })
    const api = new FakeSyncApi()
    const conflict: SyncConflict = {
      name: 'shared.txt',
      remoteID: 42,
      localSize: 20,
      localMtime: 0,
      remoteSize: 99,
      remoteModifiedAt: '',
      remoteEditedBy: 'bob',
    }

    const entries = await resolveKeepLocal(dir.asHandle(), conflict, {}, api)

    expect(api.deletedIds).toEqual([42])
    expect(api.uploadCallCount).toBe(1)
    expect(api.uploadedContentByName.get('shared.txt')).toBe('keep the local edit')
    expect(entries['shared.txt']).toBeDefined()
  })

  it('resolveKeepRemote overwrites the local file with the server version', async () => {
    const dir = new FakeDirectory({ 'shared.txt': { content: 'my local edit' } })
    const api = new FakeSyncApi()
    api.remoteFiles = [
      {
        id: 42,
        name: 'shared.txt',
        extension: 'txt',
        size: 14,
        createdAt: '',
        modifiedAt: '',
        uploadedBy: 'bob',
        editedBy: 'bob',
      },
    ]
    api.downloadContentById.set(42, 'server content')
    const conflict: SyncConflict = {
      name: 'shared.txt',
      remoteID: 42,
      localSize: 13,
      localMtime: 0,
      remoteSize: 14,
      remoteModifiedAt: '',
      remoteEditedBy: 'bob',
    }

    const entries = await resolveKeepRemote(dir.asHandle(), conflict, {}, api)

    expect(await dir.textOf('shared.txt')).toBe('server content')
    expect(entries['shared.txt']?.remoteID).toBe(42)
  })

  it('resolveKeepBoth preserves the local copy under a renamed file and adopts the remote version for the original name', async () => {
    const dir = new FakeDirectory({ 'shared.txt': { content: 'my local edit' } })
    const api = new FakeSyncApi()
    api.downloadContentById.set(42, 'server content')
    const conflict: SyncConflict = {
      name: 'shared.txt',
      remoteID: 42,
      localSize: 13,
      localMtime: 0,
      remoteSize: 14,
      remoteModifiedAt: '',
      remoteEditedBy: 'bob',
    }

    const entries = await resolveKeepBoth(dir.asHandle(), conflict, {}, api)

    const copyName = conflictCopyName('shared.txt')
    expect(await dir.textOf(copyName)).toBe('my local edit')
    expect(api.uploadedContentByName.get(copyName)).toBe('my local edit')
    expect(await dir.textOf('shared.txt')).toBe('server content')
    expect(entries[copyName]).toBeDefined()
    expect(entries['shared.txt']?.remoteID).toBe(42)
  })
})

describe('sync/reconcile conflictCopyName', () => {
  it('preserves the extension and inserts a date', () => {
    const fixedDate = new Date(2026, 8, 11) // month is 0-indexed: 8 = September

    expect(conflictCopyName('Program.cs', fixedDate)).toBe('Program (conflict copy, 2026-09-11).cs')
    expect(conflictCopyName('README', fixedDate)).toBe('README (conflict copy, 2026-09-11)')
  })
})
