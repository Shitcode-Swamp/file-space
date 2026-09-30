import { sha256Hex } from './hash'

/** One file's locally-scanned metadata, plus the handle needed later to
 * overwrite or remove it without looking it up by name again. */
export interface LocalFileEntry {
  handle: FileSystemFileHandle
  size: number
  /** Milliseconds since epoch — File.lastModified's own unit. */
  mtime: number
  sha256: string
}

export interface ScanResult {
  entries: Map<string, LocalFileEntry>
  unreadableNames: Set<string>
}

/**
 * Shallow (non-recursive) scan of a chosen folder — files in this app are
 * flat, matching REQUIREMENTS.md's model and desktop's FolderScanner.scan.
 * Skips dotfiles and subdirectories. A file that exists but can't be read
 * (a permission hiccup, a removed-mid-scan file, ...) is reported in
 * `unreadableNames` rather than silently dropped, so callers can tell "not
 * here" apart from "couldn't check" — the same distinction desktop's
 * FolderScanner draws, since collapsing the two would make reconcile()
 * mistake an unreadable file for one the user deleted, and delete its
 * remote copy for no reason.
 */
export async function scan(directoryHandle: FileSystemDirectoryHandle): Promise<ScanResult> {
  const entries = new Map<string, LocalFileEntry>()
  const unreadableNames = new Set<string>()

  for await (const handle of directoryHandle.values()) {
    if (handle.kind !== 'file') continue
    if (handle.name.startsWith('.')) continue

    try {
      const fileHandle = handle as FileSystemFileHandle
      const file = await fileHandle.getFile()
      const buffer = await file.arrayBuffer()
      entries.set(handle.name, {
        handle: fileHandle,
        size: file.size,
        mtime: file.lastModified,
        sha256: await sha256Hex(buffer),
      })
    } catch {
      unreadableNames.add(handle.name)
    }
  }

  return { entries, unreadableNames }
}
