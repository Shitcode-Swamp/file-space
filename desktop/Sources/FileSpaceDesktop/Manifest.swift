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
    /// Shallow (non-recursive) scan — files in this app are flat, matching
    /// REQUIREMENTS.md's model. Skips dotfiles (e.g. .DS_Store) and anything
    /// that isn't a regular file.
    static func scan(folderPath: String) -> [String: LocalFileEntry] {
        let folderURL = URL(fileURLWithPath: folderPath)
        guard let items = try? FileManager.default.contentsOfDirectory(
            at: folderURL,
            includingPropertiesForKeys: [.isRegularFileKey, .contentModificationDateKey],
            options: [.skipsHiddenFiles]
        ) else {
            return [:]
        }

        var result: [String: LocalFileEntry] = [:]
        for item in items {
            guard let values = try? item.resourceValues(forKeys: [.isRegularFileKey, .contentModificationDateKey]),
                  values.isRegularFile == true,
                  let data = try? Data(contentsOf: item)
            else { continue }

            result[item.lastPathComponent] = LocalFileEntry(
                size: Int64(data.count),
                mtime: values.contentModificationDate?.timeIntervalSince1970 ?? 0,
                sha256: sha256Hex(data)
            )
        }
        return result
    }

    static func sha256Hex(_ data: Data) -> String {
        SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }
}
