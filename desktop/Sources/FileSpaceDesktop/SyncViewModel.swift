// Orchestrates folder sync (REQUIREMENTS.md §5.4/§6): FSEvents-equivalent
// local watch + periodic remote poll, reconciled against the backend's
// actual capabilities.
//
// The server compares a submitted local manifest against its own per-file
// hashes (backend/internal/service/sync.go's SyncService.Diff), so
// reconcile() scans the folder and sends that manifest *before* asking for
// a diff, and can get back a genuine "conflict" action — not just
// "download" / "delete_local". Otherwise this client:
//   - uploads a local file the first time it sees one that isn't already
//     known-synced and isn't already present remotely under that name;
//   - on a local content change to an already-synced file, deletes the old
//     remote copy and re-uploads it as a fresh row, since there is no
//     update-in-place endpoint (POST always creates, there is no PUT/PATCH);
//   - on a local deletion of an already-synced file, deletes it remotely.
// A conflict is never auto-resolved: it's recorded in `conflicts` and left
// for the user (resolveKeepLocal/resolveKeepRemote/resolveKeepBoth below),
// and its name is excluded from that cycle's auto-upload/auto-delete pass so
// the client doesn't race the very decision it's waiting on.
import AppKit
import Foundation

struct SyncLogLine: Identifiable {
    let id = UUID()
    let text: String
}

/// A file that changed both locally and remotely since the last successful
/// sync (REQUIREMENTS.md §6.2's "exists, hash differs" case), surfaced for
/// the user to resolve per §6.5 rather than picked automatically.
struct SyncConflict: Identifiable, Equatable {
    var id: String { name } // filenames are unique within a synced folder
    let name: String
    let remoteID: Int64
    let localSize: Int64
    let localModifiedAt: Date
    let remoteSize: Int64
    let remoteModifiedAt: Date
    let remoteEditedBy: String
}

@MainActor
final class SyncViewModel: ObservableObject {
    @Published private(set) var folderPath: String?
    @Published private(set) var isRunning = false
    /// True only for the duration of a single reconcile() call, so the UI
    /// can show a spinner while a sync is actually in flight rather than
    /// just the static "Watching <folder>" text between polls.
    @Published private(set) var isSyncing = false
    @Published private(set) var statusMessage = "Not syncing"
    @Published private(set) var log: [SyncLogLine] = []
    /// Files changed on both sides since the last sync, waiting on the user
    /// to pick a resolution. Excluded from auto-upload/auto-delete until
    /// resolved — see the module header.
    @Published private(set) var conflicts: [SyncConflict] = []

    private let api: APIClientProtocol
    private let onUnauthorized: @MainActor () -> Void

    private var state: SyncState
    private var watcher: DirectoryWatcher?
    private var pollTask: Task<Void, Never>?
    private var debounceTask: Task<Void, Never>?

    init(api: APIClientProtocol, onUnauthorized: @escaping @MainActor () -> Void) {
        self.api = api
        self.onUnauthorized = onUnauthorized
        self.state = SyncState.load()
        self.folderPath = state.folderPath
    }

    func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.prompt = "Choose"
        guard panel.runModal() == .OK, let url = panel.url else { return }

        stop()
        selectFolder(path: url.path)
    }

    /// The non-UI half of chooseFolder(), split out so it's unit-testable
    /// without an NSOpenPanel.
    ///
    /// state.entries/lastSyncedVersion describe *a* folder's sync
    /// bookkeeping, but the sidecar is a single file keyed only by filename
    /// -- switching to a different folder without resetting them would make
    /// reconcile() think this new (possibly empty) folder already has
    /// whatever files were known-synced from the old one. At best that
    /// silently blocks their download (the version-cursor diff has nothing
    /// "new" to report); at worst the "removed locally" pass reads their
    /// absence from the new folder as a local deletion and deletes them
    /// *remotely*. Re-selecting the same path intentionally keeps its
    /// progress.
    func selectFolder(path: String) {
        if state.folderPath != path {
            state.lastSyncedVersion = 0
            state.entries = [:]
            conflicts = []
        }
        folderPath = path
        state.folderPath = path
        state.save()
    }

    func start() {
        guard let folderPath, !isRunning else { return }
        isRunning = true
        statusMessage = "Watching \(folderPath)"

        watcher = DirectoryWatcher(path: folderPath) { [weak self] in
            Task { @MainActor in self?.scheduleReconcile() }
        }
        watcher?.start()

        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                await self.reconcile()
                try? await Task.sleep(for: .seconds(45))
            }
        }
    }

    func stop() {
        isRunning = false
        watcher?.stop()
        watcher = nil
        pollTask?.cancel()
        pollTask = nil
        debounceTask?.cancel()
        debounceTask = nil
        if statusMessage != "Not syncing" {
            statusMessage = "Stopped"
        }
    }

    private static let mtimeFormatter = ISO8601DateFormatter()

    private func scheduleReconcile() {
        debounceTask?.cancel()
        debounceTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1.5))
            guard let self, !Task.isCancelled else { return }
            await self.reconcile()
        }
    }

    func reconcile() async {
        // Without this guard, the 45s poll loop and a debounced FSEvents
        // callback can both call reconcile() as separate Task chains, and
        // since this is an async function full of await points, the actor
        // can freely interleave a second call while the first is still
        // suspended on network I/O. Both calls then read/mutate the same
        // `state` property from their own, independently-captured folder
        // scans -- a file the *other* call just uploaded (and so added to
        // `state.entries`) looks, from this call's stale scan, like a file
        // that "used to be known but is now missing locally," and gets
        // deleted *remotely* for it. That remote deletion then comes back
        // as a genuine delete_local action on the next cycle, deleting the
        // file locally too -- which is the moment the mistake becomes
        // visible. Refusing to run a second reconcile concurrently is what
        // actually prevents this; the 45s poll and the next debounced watch
        // event are enough of a safety net to not lose the skipped work.
        guard let folderPath, !isSyncing else { return }
        isSyncing = true
        let startedAt = Date()
        defer {
            Task { @MainActor [weak self] in
                // A no-op cycle (nothing changed on either side) can finish
                // in well under 100ms -- too fast for a human to ever
                // perceive the spinner turning on at all. Holding it visible
                // for at least this long is what actually makes "a sync
                // just happened" observable, not just theoretically correct.
                let elapsed = Date().timeIntervalSince(startedAt)
                let minimumVisible = 0.6
                if elapsed < minimumVisible {
                    try? await Task.sleep(for: .seconds(minimumVisible - elapsed))
                }
                self?.isSyncing = false
            }
        }
        do {
            // Scan first: the manifest built from this scan is what lets the
            // server detect a real conflict instead of only ever reporting
            // "download" (backend/internal/service/sync.go).
            let scanResult = try FolderScanner.scan(folderPath: folderPath)
            let localEntries = scanResult.entries
            if !scanResult.unreadableNames.isEmpty {
                appendLog("Skipping \(scanResult.unreadableNames.count) unreadable file(s), left untouched: \(scanResult.unreadableNames.sorted().joined(separator: ", "))")
            }
            let manifest = localEntries.map { name, entry in
                SyncManifestEntryDTO(
                    name: name, size: entry.size,
                    mtime: Self.mtimeFormatter.string(from: Date(timeIntervalSince1970: entry.mtime)),
                    sha256: entry.sha256
                )
            }

            let remoteFiles = try await api.listFiles()
            let remoteByName = Dictionary(uniqueKeysWithValues: remoteFiles.map { ($0.name, $0) })

            let diff = try await api.syncDiff(lastSyncedVersion: state.lastSyncedVersion, manifest: manifest)
            // Conflicts already recorded from a previous cycle and still
            // unresolved must keep blocking auto-upload/auto-delete too, not
            // just ones this cycle just discovered.
            var conflictedNames = Set(conflicts.map(\.name))
            // localEntries was captured by the scan above, *before* any
            // delete_local action below removes a file from disk -- without
            // tracking those names here too, the upload loop further down
            // would still see the (now-stale) snapshot and re-upload a file
            // the server just told us was deleted.
            var deletedByServerNames: Set<String> = []
            // Names written to disk by a "download" action *this* cycle --
            // localEntries is a snapshot taken before this loop runs, so a
            // freshly-downloaded file is in state.entries but not in
            // localEntries yet. Without tracking that gap here too, the
            // removedLocally check below reads "known, but absent from the
            // pre-download scan" as "the user deleted it locally" and
            // deletes the file right back off the server, in the very same
            // cycle that just downloaded it.
            var downloadedNames: Set<String> = []
            for action in diff.actions {
                switch action.action {
                case SyncActionKind.download:
                    guard let remote = remoteByName[action.name] else { continue }
                    let (data, _) = try await api.downloadFile(id: remote.id)
                    let destination = URL(fileURLWithPath: folderPath).appendingPathComponent(action.name)
                    try data.write(to: destination)
                    state.entries[action.name] = SyncedFileEntry(
                        remoteID: remote.id, size: Int64(data.count),
                        mtime: (try? destination.resourceValues(forKeys: [.contentModificationDateKey]))?
                            .contentModificationDate?.timeIntervalSince1970 ?? 0,
                        sha256: FolderScanner.sha256Hex(data)
                    )
                    downloadedNames.insert(action.name)
                    appendLog("Downloaded \(action.name)")
                case SyncActionKind.deleteLocal:
                    let target = URL(fileURLWithPath: folderPath).appendingPathComponent(action.name)
                    try? FileManager.default.removeItem(at: target)
                    state.entries.removeValue(forKey: action.name)
                    deletedByServerNames.insert(action.name)
                    appendLog("Removed local \(action.name) (deleted remotely)")
                case SyncActionKind.conflict:
                    guard let details = action.conflict, let local = localEntries[action.name] else { continue }
                    let remoteID = remoteByName[action.name]?.id ?? state.entries[action.name]?.remoteID
                    guard let remoteID else { continue }
                    let conflict = SyncConflict(
                        name: action.name, remoteID: remoteID,
                        localSize: local.size, localModifiedAt: Date(timeIntervalSince1970: local.mtime),
                        remoteSize: details.remoteSize, remoteModifiedAt: details.remoteModifiedAt,
                        remoteEditedBy: details.remoteEditedBy
                    )
                    if let existingIndex = conflicts.firstIndex(where: { $0.name == conflict.name }) {
                        conflicts[existingIndex] = conflict
                    } else {
                        conflicts.append(conflict)
                    }
                    conflictedNames.insert(action.name)
                    appendLog("\(action.name) changed on both sides — needs your decision")
                default:
                    break
                }
            }
            state.lastSyncedVersion = diff.newVersion

            for (name, local) in localEntries {
                guard !conflictedNames.contains(name), !deletedByServerNames.contains(name) else { continue }
                if let known = state.entries[name] {
                    guard known.size != local.size || known.sha256 != local.sha256 else { continue }
                    try? await api.deleteFile(id: known.remoteID)
                    let fileURL = URL(fileURLWithPath: folderPath).appendingPathComponent(name)
                    let created = try await api.uploadFile(fileURL: fileURL)
                    state.entries[name] = SyncedFileEntry(remoteID: created.id, size: local.size, mtime: local.mtime, sha256: local.sha256)
                    appendLog("Re-uploaded changed file \(name)")
                } else if remoteByName[name] == nil {
                    let fileURL = URL(fileURLWithPath: folderPath).appendingPathComponent(name)
                    let created = try await api.uploadFile(fileURL: fileURL)
                    state.entries[name] = SyncedFileEntry(remoteID: created.id, size: local.size, mtime: local.mtime, sha256: local.sha256)
                    appendLog("Uploaded new file \(name)")
                }
            }

            // A name only counts as "removed locally" (and so gets deleted
            // remotely) if it's genuinely absent from the folder -- a file
            // that's merely unreadable this cycle (locked, an un-downloaded
            // iCloud placeholder, a permissions hiccup) is left alone rather
            // than treated as a deletion. See FolderScanner.ScanResult.
            let removedLocally = state.entries.keys.filter {
                localEntries[$0] == nil && !scanResult.unreadableNames.contains($0)
                    && !conflictedNames.contains($0) && !downloadedNames.contains($0)
            }
            for name in removedLocally {
                guard let known = state.entries[name] else { continue }
                try? await api.deleteFile(id: known.remoteID)
                state.entries.removeValue(forKey: name)
                appendLog("Deleted remote \(name) (removed locally)")
            }

            state.save()
            statusMessage = "Synced at \(Date().formatted(date: .omitted, time: .standard))"
        } catch let error as APIError where error.isUnauthorized {
            onUnauthorized()
        } catch {
            statusMessage = "Sync error: \(error.localizedDescription)"
        }
    }

    private func appendLog(_ message: String) {
        log.append(SyncLogLine(text: message))
        if log.count > 50 {
            log.removeFirst(log.count - 50)
        }
    }

    // MARK: - Conflict resolution (REQUIREMENTS.md §6.5)

    /// Overwrite the remote copy with what's on disk — deletes the old
    /// remote row (there is no update-in-place endpoint, see the module
    /// header) and re-uploads the local content under the same name.
    func resolveKeepLocal(_ conflict: SyncConflict) async {
        guard let folderPath else { return }
        let fileURL = URL(fileURLWithPath: folderPath).appendingPathComponent(conflict.name)
        do {
            try? await api.deleteFile(id: conflict.remoteID)
            let created = try await api.uploadFile(fileURL: fileURL)
            let data = try Data(contentsOf: fileURL)
            state.entries[conflict.name] = SyncedFileEntry(
                remoteID: created.id, size: Int64(data.count),
                mtime: (try? fileURL.resourceValues(forKeys: [.contentModificationDateKey]))?
                    .contentModificationDate?.timeIntervalSince1970 ?? 0,
                sha256: FolderScanner.sha256Hex(data)
            )
            state.save()
            conflicts.removeAll { $0.name == conflict.name }
            appendLog("Kept local version of \(conflict.name)")
        } catch {
            appendLog("Failed to keep local version of \(conflict.name): \(error.localizedDescription)")
        }
    }

    /// Overwrite the local copy with the server's version, discarding the
    /// local edit that caused the conflict.
    func resolveKeepRemote(_ conflict: SyncConflict) async {
        guard let folderPath else { return }
        let fileURL = URL(fileURLWithPath: folderPath).appendingPathComponent(conflict.name)
        do {
            let (data, _) = try await api.downloadFile(id: conflict.remoteID)
            try data.write(to: fileURL)
            state.entries[conflict.name] = SyncedFileEntry(
                remoteID: conflict.remoteID, size: Int64(data.count),
                mtime: (try? fileURL.resourceValues(forKeys: [.contentModificationDateKey]))?
                    .contentModificationDate?.timeIntervalSince1970 ?? 0,
                sha256: FolderScanner.sha256Hex(data)
            )
            state.save()
            conflicts.removeAll { $0.name == conflict.name }
            appendLog("Kept remote version of \(conflict.name)")
        } catch {
            appendLog("Failed to keep remote version of \(conflict.name): \(error.localizedDescription)")
        }
    }

    /// Preserves both edits, matching the Dropbox/Google Drive pattern
    /// REQUIREMENTS.md §6.5 calls for: the local edit is renamed to a
    /// "conflict copy" and uploaded as a new file, while the original name
    /// is brought in line with the server's version on both sides.
    func resolveKeepBoth(_ conflict: SyncConflict) async {
        guard let folderPath else { return }
        let originalURL = URL(fileURLWithPath: folderPath).appendingPathComponent(conflict.name)
        let copyName = Self.conflictCopyName(for: conflict.name)
        let copyURL = URL(fileURLWithPath: folderPath).appendingPathComponent(copyName)

        do {
            try FileManager.default.copyItem(at: originalURL, to: copyURL)
            let uploadedCopy = try await api.uploadFile(fileURL: copyURL)
            let copyData = try Data(contentsOf: copyURL)
            state.entries[copyName] = SyncedFileEntry(
                remoteID: uploadedCopy.id, size: Int64(copyData.count),
                mtime: (try? copyURL.resourceValues(forKeys: [.contentModificationDateKey]))?
                    .contentModificationDate?.timeIntervalSince1970 ?? 0,
                sha256: FolderScanner.sha256Hex(copyData)
            )

            let (remoteData, _) = try await api.downloadFile(id: conflict.remoteID)
            try remoteData.write(to: originalURL)
            state.entries[conflict.name] = SyncedFileEntry(
                remoteID: conflict.remoteID, size: Int64(remoteData.count),
                mtime: (try? originalURL.resourceValues(forKeys: [.contentModificationDateKey]))?
                    .contentModificationDate?.timeIntervalSince1970 ?? 0,
                sha256: FolderScanner.sha256Hex(remoteData)
            )

            state.save()
            conflicts.removeAll { $0.name == conflict.name }
            appendLog("Kept both versions of \(conflict.name) (local copy saved as \(copyName))")
        } catch {
            try? FileManager.default.removeItem(at: copyURL)
            appendLog("Failed to keep both versions of \(conflict.name): \(error.localizedDescription)")
        }
    }

    private static let conflictCopyDateFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter
    }()

    /// "Program.cs" -> "Program (conflict copy, 2026-09-11).cs", matching
    /// the Dropbox/Google Drive convention REQUIREMENTS.md §6.5 references.
    static func conflictCopyName(for name: String, today: Date = Date()) -> String {
        let dateSuffix = conflictCopyDateFormatter.string(from: today)
        let nsName = name as NSString
        let ext = nsName.pathExtension
        let base = nsName.deletingPathExtension
        let taggedBase = "\(base) (conflict copy, \(dateSuffix))"
        return ext.isEmpty ? taggedBase : "\(taggedBase).\(ext)"
    }
}
