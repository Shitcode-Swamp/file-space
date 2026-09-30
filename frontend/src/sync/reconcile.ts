// Direct port of desktop/Sources/FileSpaceDesktop/SyncViewModel.swift's
// reconcile() and resolveKeepLocal/Remote/Both, triggered by a single
// "Sync now" button click instead of an FSEvents watcher + poll loop
// (REQUIREMENTS.md §5.3/§6.4: browsers can't watch a folder persistently).
//
// The server compares a submitted local manifest against its own per-file
// hashes (backend/internal/service/sync.go's SyncService.Diff), so this
// scans the folder and sends that manifest *before* asking for a diff, and
// can get back a genuine "conflict" action, not just "download"/
// "delete_local". Otherwise:
//   - a local file is uploaded the first time it's seen and isn't already
//     known-synced or present remotely under that name;
//   - a local content change to an already-synced file deletes the old
//     remote copy and re-uploads it as a fresh row (no update-in-place
//     endpoint — POST always creates);
//   - a local deletion of an already-synced file deletes it remotely.
// A conflict is never auto-resolved: it's returned in `conflicts` for the
// caller to store and later resolve via resolveKeepLocal/Remote/Both, and
// its name is excluded from this cycle's auto-upload/auto-delete pass.
//
// This module is deliberately a plain, storage-and-React-free function:
// SyncContext.tsx owns persisting the returned state to storage.ts and
// invalidating the TanStack Query file-list cache — reconcile() itself
// only computes what changed.

import { scan } from './folderScanner'
import { sha256Hex } from './hash'
import type { SyncedFileEntry, SyncState } from './storage'
import type { ApiFileRecord, ListFilesResult, SyncDiffResponse, SyncManifestEntry } from '../api/types'

/** A file that changed both locally and remotely since the last successful
 * sync (REQUIREMENTS.md §6.2's "exists, hash differs" case), surfaced for
 * the user to resolve per §6.5 rather than picked automatically. Mirrors
 * desktop's SyncConflict. */
export interface SyncConflict {
  name: string
  remoteID: number
  localSize: number
  localMtime: number
  remoteSize: number
  remoteModifiedAt: string
  remoteEditedBy: string
}

/** The subset of the API client reconcile()/resolve* need, matching
 * api/client.ts's real exported functions exactly — lets tests inject a
 * fake in place of real fetch calls, mirroring desktop's
 * APIClientProtocol seam. */
export interface ReconcileApi {
  listFiles: (params?: { limit?: number }) => Promise<ListFilesResult>
  syncDiff: (lastSyncedVersion: number, manifest: SyncManifestEntry[]) => Promise<SyncDiffResponse>
  downloadFile: (id: number) => Promise<{ blob: Blob; filename: string | null }>
  uploadFileInChunks: (
    file: File,
    onProgress?: (loaded: number, total: number) => void,
  ) => Promise<ApiFileRecord>
  deleteFile: (id: number) => Promise<void>
}

export interface ReconcileResult {
  entries: Record<string, SyncedFileEntry>
  lastSyncedVersion: number
  conflicts: SyncConflict[]
  log: string[]
}

async function writeFile(
  directoryHandle: FileSystemDirectoryHandle,
  name: string,
  content: Blob,
): Promise<void> {
  const fileHandle = await directoryHandle.getFileHandle(name, { create: true })
  const writable = await fileHandle.createWritable()
  await writable.write(content)
  await writable.close()
}

async function entryFromFile(file: File, remoteID: number): Promise<SyncedFileEntry> {
  const sha256 = await sha256Hex(await file.arrayBuffer())
  return { remoteID, size: file.size, mtime: file.lastModified, sha256 }
}

export async function reconcile(
  directoryHandle: FileSystemDirectoryHandle,
  currentState: SyncState,
  currentConflicts: SyncConflict[],
  api: ReconcileApi,
): Promise<ReconcileResult> {
  const log: string[] = []

  // Scan first: the manifest built from this scan is what lets the server
  // detect a real conflict instead of only ever reporting "download".
  const { entries: localEntries, unreadableNames } = await scan(directoryHandle)
  if (unreadableNames.size > 0) {
    log.push(
      `Skipping ${unreadableNames.size} unreadable file(s), left untouched: ${Array.from(unreadableNames).sort().join(', ')}`,
    )
  }

  const manifest: SyncManifestEntry[] = Array.from(localEntries.entries()).map(([name, entry]) => ({
    name,
    size: entry.size,
    mtime: new Date(entry.mtime).toISOString(),
    sha256: entry.sha256,
  }))

  const { files: remoteFiles } = await api.listFiles({ limit: 1000 })
  const remoteByName = new Map(remoteFiles.map((f) => [f.name, f] as const))

  const diff = await api.syncDiff(currentState.lastSyncedVersion, manifest)

  const newEntries: Record<string, SyncedFileEntry> = { ...currentState.entries }
  const newConflicts: SyncConflict[] = [...currentConflicts]
  // Conflicts already recorded from a previous cycle and still unresolved
  // must keep blocking auto-upload/auto-delete too, not just ones this
  // cycle just discovered.
  const conflictedNames = new Set(currentConflicts.map((c) => c.name))
  // localEntries was captured by the scan above, *before* any delete_local
  // action below removes a file from disk -- without tracking those names
  // here too, the upload pass further down would still see the (now-stale)
  // snapshot and re-upload a file the server just told us was deleted.
  const deletedByServerNames = new Set<string>()
  // Names written to disk by a "download" action *this* cycle --
  // localEntries is a snapshot taken before this loop runs, so a
  // freshly-downloaded file is in newEntries but not in localEntries yet.
  // Without tracking that gap here too, the removedLocally check below
  // reads "known, but absent from the pre-download scan" as "the user
  // deleted it locally" and deletes the file right back off the server, in
  // the very same cycle that just downloaded it.
  const downloadedNames = new Set<string>()

  for (const action of diff.actions) {
    if (action.action === 'download') {
      const remote = remoteByName.get(action.name)
      if (!remote) continue
      const { blob } = await api.downloadFile(remote.id)
      await writeFile(directoryHandle, action.name, blob)
      const written = await (await directoryHandle.getFileHandle(action.name)).getFile()
      newEntries[action.name] = await entryFromFile(written, remote.id)
      downloadedNames.add(action.name)
      log.push(`Downloaded ${action.name}`)
    } else if (action.action === 'delete_local') {
      try {
        await directoryHandle.removeEntry(action.name)
      } catch {
        // best-effort, matches desktop's try?
      }
      delete newEntries[action.name]
      deletedByServerNames.add(action.name)
      log.push(`Removed local ${action.name} (deleted remotely)`)
    } else if (action.action === 'conflict') {
      const details = action.conflict
      const local = localEntries.get(action.name)
      if (!details || !local) continue
      const remoteID = remoteByName.get(action.name)?.id ?? newEntries[action.name]?.remoteID
      if (remoteID === undefined) continue
      const conflict: SyncConflict = {
        name: action.name,
        remoteID,
        localSize: local.size,
        localMtime: local.mtime,
        remoteSize: details.remoteSize,
        remoteModifiedAt: details.remoteModifiedAt,
        remoteEditedBy: details.remoteEditedBy,
      }
      const existingIndex = newConflicts.findIndex((c) => c.name === conflict.name)
      if (existingIndex >= 0) {
        newConflicts[existingIndex] = conflict
      } else {
        newConflicts.push(conflict)
      }
      conflictedNames.add(action.name)
      log.push(`${action.name} changed on both sides — needs your decision`)
    }
  }

  for (const [name, local] of localEntries) {
    if (conflictedNames.has(name) || deletedByServerNames.has(name)) continue
    const known = newEntries[name]
    if (known) {
      if (known.size === local.size && known.sha256 === local.sha256) continue
      try {
        await api.deleteFile(known.remoteID)
      } catch {
        // best-effort, matches desktop's try?
      }
      const file = await local.handle.getFile()
      const created = await api.uploadFileInChunks(file)
      newEntries[name] = { remoteID: created.id, size: local.size, mtime: local.mtime, sha256: local.sha256 }
      log.push(`Re-uploaded changed file ${name}`)
    } else if (!remoteByName.has(name)) {
      const file = await local.handle.getFile()
      const created = await api.uploadFileInChunks(file)
      newEntries[name] = { remoteID: created.id, size: local.size, mtime: local.mtime, sha256: local.sha256 }
      log.push(`Uploaded new file ${name}`)
    }
  }

  // A name only counts as "removed locally" (and so gets deleted remotely)
  // if it's genuinely absent from the folder -- a file that's merely
  // unreadable this cycle (locked, a permissions hiccup, ...) is left
  // alone rather than treated as a deletion.
  const removedLocally = Object.keys(newEntries).filter(
    (name) =>
      !localEntries.has(name) &&
      !unreadableNames.has(name) &&
      !conflictedNames.has(name) &&
      !downloadedNames.has(name),
  )
  for (const name of removedLocally) {
    const known = newEntries[name]
    if (!known) continue
    try {
      await api.deleteFile(known.remoteID)
    } catch {
      // best-effort, matches desktop's try?
    }
    delete newEntries[name]
    log.push(`Deleted remote ${name} (removed locally)`)
  }

  return { entries: newEntries, lastSyncedVersion: diff.newVersion, conflicts: newConflicts, log }
}

// ---- Conflict resolution (REQUIREMENTS.md §6.5) -------------------------
// Each function returns the full updated entries record on success, or
// throws on failure — callers (SyncContext.tsx) are responsible for only
// removing the conflict from their list once the call actually succeeds.

/** Overwrite the remote copy with what's on disk — deletes the old remote
 * row (there is no update-in-place endpoint, see the module header) and
 * re-uploads the local content under the same name. */
export async function resolveKeepLocal(
  directoryHandle: FileSystemDirectoryHandle,
  conflict: SyncConflict,
  entries: Record<string, SyncedFileEntry>,
  api: ReconcileApi,
): Promise<Record<string, SyncedFileEntry>> {
  const fileHandle = await directoryHandle.getFileHandle(conflict.name)
  try {
    await api.deleteFile(conflict.remoteID)
  } catch {
    // best-effort, matches desktop's try?
  }
  const file = await fileHandle.getFile()
  const created = await api.uploadFileInChunks(file)
  return { ...entries, [conflict.name]: await entryFromFile(file, created.id) }
}

/** Overwrite the local copy with the server's version, discarding the
 * local edit that caused the conflict. */
export async function resolveKeepRemote(
  directoryHandle: FileSystemDirectoryHandle,
  conflict: SyncConflict,
  entries: Record<string, SyncedFileEntry>,
  api: ReconcileApi,
): Promise<Record<string, SyncedFileEntry>> {
  const { blob } = await api.downloadFile(conflict.remoteID)
  await writeFile(directoryHandle, conflict.name, blob)
  const written = await (await directoryHandle.getFileHandle(conflict.name)).getFile()
  return { ...entries, [conflict.name]: await entryFromFile(written, conflict.remoteID) }
}

/** Preserves both edits, matching the Dropbox/Google Drive pattern
 * REQUIREMENTS.md §6.5 calls for: the local edit is renamed to a
 * "conflict copy" and uploaded as a new file, while the original name is
 * brought in line with the server's version on both sides. */
export async function resolveKeepBoth(
  directoryHandle: FileSystemDirectoryHandle,
  conflict: SyncConflict,
  entries: Record<string, SyncedFileEntry>,
  api: ReconcileApi,
): Promise<Record<string, SyncedFileEntry>> {
  const copyName = conflictCopyName(conflict.name)
  try {
    const originalFile = await (await directoryHandle.getFileHandle(conflict.name)).getFile()
    await writeFile(directoryHandle, copyName, originalFile)
    const copyFile = await (await directoryHandle.getFileHandle(copyName)).getFile()
    const uploadedCopy = await api.uploadFileInChunks(copyFile)
    const copyEntry = await entryFromFile(copyFile, uploadedCopy.id)

    const { blob: remoteBlob } = await api.downloadFile(conflict.remoteID)
    await writeFile(directoryHandle, conflict.name, remoteBlob)
    const overwritten = await (await directoryHandle.getFileHandle(conflict.name)).getFile()
    const originalEntry = await entryFromFile(overwritten, conflict.remoteID)

    return { ...entries, [copyName]: copyEntry, [conflict.name]: originalEntry }
  } catch (err) {
    await directoryHandle.removeEntry(copyName).catch(() => {})
    throw err
  }
}

function pad2(n: number): string {
  return n.toString().padStart(2, '0')
}

/** "Program.cs" -> "Program (conflict copy, 2026-09-11).cs", matching the
 * Dropbox/Google Drive convention REQUIREMENTS.md §6.5 references and
 * desktop's SyncViewModel.conflictCopyName(for:today:). Uses the browser's
 * local date (not UTC) so the suffix matches the user's own calendar day. */
export function conflictCopyName(name: string, today: Date = new Date()): string {
  const dateSuffix = `${today.getFullYear()}-${pad2(today.getMonth() + 1)}-${pad2(today.getDate())}`
  const dotIndex = name.lastIndexOf('.')
  if (dotIndex <= 0) {
    return `${name} (conflict copy, ${dateSuffix})`
  }
  const base = name.slice(0, dotIndex)
  const ext = name.slice(dotIndex + 1)
  return `${base} (conflict copy, ${dateSuffix}).${ext}`
}
