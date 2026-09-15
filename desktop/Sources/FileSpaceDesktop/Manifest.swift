// Local manifest computation and the sidecar file tracking sync state
// (REQUIREMENTS.md §6.1/§6.3: "{name, size, mtime, sha256}" per file, plus a
// per-client last-synced version). Persisted as JSON under Application
// Support rather than UserDefaults/plist, since it's structured and can grow
// with the number of synced files.

import CryptoKit
import Foundation

struct LocalFileEntry: Equatable {
    let size: Int64
    let mtime: Double
    let sha256: String
}

struct SyncedFileEntry: Codable {
    var remoteID: Int64
    var size: Int64
    var mtime: Double
    var sha256: String
}

struct SyncState: Codable {
    var folderPath: String?
    var lastSyncedVersion: Int64 = 0
    var entries: [String: SyncedFileEntry] = [:]

    private static var fileURL: URL {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("FileSpaceDesktop", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("sync-state.json")
    }

    static func load() -> SyncState {
        guard let data = try? Data(contentsOf: fileURL),
              let state = try? JSONDecoder().decode(SyncState.self, from: data)
        else {
            return SyncState()
        }
        return state
    }

    func save() {
        guard let data = try? JSONEncoder().encode(self) else { return }
        try? data.write(to: Self.fileURL)
    }
}

enum FolderScanner {
    /// A scan's result: entries for every file that could actually be read,
    /// plus the names of any that exist but couldn't be (permission denied,
    /// locked, an un-downloaded iCloud placeholder, ...). The distinction
    /// matters to SyncViewModel.reconcile(): a name missing from `entries`
    /// only means "removed locally" (and so gets deleted remotely too) if
    /// it's *also* absent from `unreadableNames` — otherwise an unreadable
    /// file would look identical to a deleted one and get its remote copy
    /// destroyed for no reason.
    struct ScanResult {
        var entries: [String: LocalFileEntry]
        var unreadableNames: Set<String>
    }

    /// Shallow (non-recursive) scan — files in this app are flat, matching
    /// REQUIREMENTS.md's model. Skips dotfiles (e.g. .DS_Store) and anything
    /// that isn't a regular file. Throws if the folder itself can't be
    /// listed at all (e.g. it was deleted, or this process lacks
    /// permission to read it) -- that must never be silently treated as
    /// "every file in it was removed," which is what returning an empty
    /// result used to do.
    static func scan(folderPath: String) throws -> ScanResult {
        let folderURL = URL(fileURLWithPath: folderPath)
        let items = try FileManager.default.contentsOfDirectory(
            at: folderURL,
            includingPropertiesForKeys: [.isRegularFileKey, .contentModificationDateKey],
            options: [.skipsHiddenFiles]
        )

        var entries: [String: LocalFileEntry] = [:]
        var unreadableNames: Set<String> = []
        for item in items {
            // Can't even stat it (broken symlink, permission denied, ...) --
            // record it as unreadable rather than silently dropping it, since
            // we can't tell it apart from "legitimately not a regular file"
            // otherwise.
            guard let values = try? item.resourceValues(forKeys: [.isRegularFileKey, .contentModificationDateKey]) else {
                unreadableNames.insert(item.lastPathComponent)
                continue
            }
            // Directories, symlinks resolved to non-files, etc. -- correctly
            // filtered out, not a failure.
            guard values.isRegularFile == true else { continue }

            guard let data = try? Data(contentsOf: item) else {
                unreadableNames.insert(item.lastPathComponent)
                continue
            }

            entries[item.lastPathComponent] = LocalFileEntry(
                size: Int64(data.count),
                mtime: values.contentModificationDate?.timeIntervalSince1970 ?? 0,
                sha256: sha256Hex(data)
            )
        }
        return ScanResult(entries: entries, unreadableNames: unreadableNames)
    }

    static func sha256Hex(_ data: Data) -> String {
        SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }
}
