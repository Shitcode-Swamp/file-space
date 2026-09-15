// Orchestrates folder sync (REQUIREMENTS.md §5.4/§6): FSEvents-equivalent
// local watch + periodic remote poll, reconciled against the backend's
// actual capabilities.
//
// The server now compares a submitted local manifest against its own
// per-file hashes (backend/internal/service/sync.go), so reconcile() scans
// the folder and sends that manifest *before* asking for a diff, and can
// receive a genuine "conflict" action back — not just "download" /
// "delete_local". A conflict is never auto-resolved: it's recorded in
// `conflicts` and left for the user (see resolveKeepLocal/KeepRemote/
// KeepBoth below), and its name is excluded from the auto-upload pass this
// cycle so the client doesn't race the very decision it's waiting on.
import AppKit
import Foundation

// MARK: - Activity log

enum SyncLogKind {
    case upload, download, deleteLocal, deleteRemote, conflict, error
}

struct SyncLogEntry: Identifiable {
    let id = UUID()
    let kind: SyncLogKind
    /// The file this entry is about, if any (some entries, like a
    /// folder-level error, aren't about a specific file).
    let name: String?
    let message: String
    let timestamp: Date
}

// MARK: - Per-file status (drives the file table's sync badge column)

enum FileSyncStatus: Equatable {
    case synced
    case uploading
    case downloading
    /// Known locally but not yet reconciled with the server this session
    /// (e.g. a brand-new file, about to be uploaded).
    case pending
    case conflict
}

// MARK: - Toolbar pill state

enum SyncPillState: Equatable {
    case notSetUp
    case paused
    case watching
    case syncing(count: Int)
    case attention(count: Int)
    case error
}

// MARK: - Conflicts

/// A file that changed both locally and remotely since the last successful
/// sync (REQUIREMENTS.md §6.2's "exists, hash differs" case), surfaced for
/// the user to resolve per §6.5 rather than picked automatically.
struct SyncConflict: Identifiable, Equatable {
    let id: String // the filename — conflicts are inherently one-per-name
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
    /// True for the duration of a single reconcile() call — distinct from
    /// isRunning (which just means the watcher/poll loop is active), so the
    /// UI can show "Syncing" only while something is actually in flight.
    @Published private(set) var isSyncingNow = false
    @Published private(set) var statusMessage = "Not syncing"
    @Published private(set) var lastSyncedAt: Date?
    @Published private(set) var lastError: String?
    @Published private(set) var log: [SyncLogEntry] = []
    @Published private(set) var conflicts: [SyncConflict] = []
    @Published private(set) var fileStatuses: [String: FileSyncStatus] = [:]

    var pillState: SyncPillState {
        guard folderPath != nil else { return .notSetUp }
        if !conflicts.isEmpty { return .attention(count: conflicts.count) }
        if lastError != nil { return .error }
        if isSyncingNow {
            let active = fileStatuses.values.filter { $0 == .uploading || $0 == .downloading }.count
            return .syncing(count: max(active, 1))
        }
        return isRunning ? .watching : .paused
    }

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
        folderPath = url.path
        state.folderPath = url.path
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

    /// Cancels the watcher/poll/debounce loop but deliberately leaves
    /// folderPath and the persisted SyncState alone — this is a pause, not
    /// a teardown, so resuming (start()) doesn't require re-choosing a
    /// folder or losing track of what's already synced.
    func stop() {
        isRunning = false
        watcher?.stop()
        watcher = nil
        pollTask?.cancel()
        pollTask = nil
        debounceTask?.cancel()
        debounceTask = nil
        if statusMessage != "Not syncing" {
            statusMessage = "Paused"
        }
    }

    private func scheduleReconcile() {
        debounceTask?.cancel()
        debounceTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1.5))
            guard let self, !Task.isCancelled else { return }
            await self.reconcile()
        }
    }

    private static let mtimeFormatter = ISO8601DateFormatter()

    func reconcile() async {
        guard let folderPath else { return }
        isSyncingNow = true
        defer { isSyncingNow = false }

        do {
            // Scan first: the manifest built from this scan is what lets the
            // server detect a real conflict instead of only ever reporting
            // "download" (backend/internal/service/sync.go).
            let scanResult = try FolderScanner.scan(folderPath: folderPath)
            let localEntries = scanResult.entries
            if !scanResult.unreadableNames.isEmpty {
                appendLog(.error, name: nil, "Skipping \(scanResult.unreadableNames.count) unreadable file(s), left untouched: \(scanResult.unreadableNames.sorted().joined(separator: ", "))")
            }
            seedBaselineStatuses(localEntries: localEntries)

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
            var conflictedNames: Set<String> = []
            // localEntries was captured by the scan above, *before* any
            // delete_local action below removes a file from disk -- without
            // tracking those names here too, the upload loop further down
            // would still see the (now-stale) snapshot and re-upload a file
            // the server just told us was deleted.
            var deletedByServerNames: Set<String> = []

            for action in diff.actions {
                switch action.action {
                case SyncActionKind.download:
                    guard let remote = remoteByName[action.name] else { continue }
                    fileStatuses[action.name] = .downloading
                    let (data, _) = try await api.downloadFile(id: remote.id)
                    let destination = URL(fileURLWithPath: folderPath).appendingPathComponent(action.name)
                    try data.write(to: destination)
                    state.entries[action.name] = SyncedFileEntry(
                        remoteID: remote.id, size: Int64(data.count),
                        mtime: (try? destination.resourceValues(forKeys: [.contentModificationDateKey]))?
                            .contentModificationDate?.timeIntervalSince1970 ?? 0,
                        sha256: FolderScanner.sha256Hex(data)
                    )
                    fileStatuses[action.name] = .synced
                    appendLog(.download, name: action.name, "Downloaded \(action.name)")

                case SyncActionKind.deleteLocal:
                    let target = URL(fileURLWithPath: folderPath).appendingPathComponent(action.name)
                    try? FileManager.default.removeItem(at: target)
                    state.entries.removeValue(forKey: action.name)
                    fileStatuses.removeValue(forKey: action.name)
                    deletedByServerNames.insert(action.name)
                    appendLog(.deleteLocal, name: action.name, "Removed local \(action.name) (deleted remotely)")

                case SyncActionKind.conflict:
                    guard let details = action.conflict, let local = localEntries[action.name] else { continue }
                    let remoteID = remoteByName[action.name]?.id ?? 0
                    let conflict = SyncConflict(
                        id: action.name, name: action.name, remoteID: remoteID,
                        localSize: local.size, localModifiedAt: Date(timeIntervalSince1970: local.mtime),
                        remoteSize: details.remoteSize, remoteModifiedAt: details.remoteModifiedAt,
                        remoteEditedBy: details.remoteEditedBy
                    )
                    if let existingIndex = conflicts.firstIndex(where: { $0.id == conflict.id }) {
                        conflicts[existingIndex] = conflict
                    } else {
                        conflicts.append(conflict)
                    }
                    conflictedNames.insert(action.name)
                    fileStatuses[action.name] = .conflict
                    appendLog(.conflict, name: action.name, "\(action.name) changed on both sides — needs your decision")

                default:
                    break
                }
            }
            state.lastSyncedVersion = diff.newVersion

            // Conflicts already recorded from a previous cycle and still
            // unresolved must keep blocking auto-upload too, not just ones
            // this cycle just discovered.
            conflictedNames.formUnion(conflicts.map(\.name))

            for (name, local) in localEntries {
                guard !conflictedNames.contains(name), !deletedByServerNames.contains(name) else { continue }
                if let known = state.entries[name] {
                    guard known.size != local.size || known.sha256 != local.sha256 else { continue }
                    fileStatuses[name] = .uploading
                    try? await api.deleteFile(id: known.remoteID)
                    let fileURL = URL(fileURLWithPath: folderPath).appendingPathComponent(name)
                    let created = try await api.uploadFile(fileURL: fileURL)
                    state.entries[name] = SyncedFileEntry(remoteID: created.id, size: local.size, mtime: local.mtime, sha256: local.sha256)
                    fileStatuses[name] = .synced
                    appendLog(.upload, name: name, "Re-uploaded changed file \(name)")
                } else if remoteByName[name] == nil {
                    fileStatuses[name] = .uploading
                    let fileURL = URL(fileURLWithPath: folderPath).appendingPathComponent(name)
                    let created = try await api.uploadFile(fileURL: fileURL)
                    state.entries[name] = SyncedFileEntry(remoteID: created.id, size: local.size, mtime: local.mtime, sha256: local.sha256)
                    fileStatuses[name] = .synced
                    appendLog(.upload, name: name, "Uploaded new file \(name)")
                }
            }

            // A name only counts as "removed locally" (and so gets deleted
            // remotely) if it's genuinely absent from the folder -- a file
            // that's merely unreadable this cycle (locked, an un-downloaded
            // iCloud placeholder, a permissions hiccup) is left alone rather
            // than treated as a deletion. See FolderScanner.ScanResult.
            let removedLocally = state.entries.keys.filter {
                localEntries[$0] == nil && !scanResult.unreadableNames.contains($0) && !conflictedNames.contains($0)
            }
            for name in removedLocally {
                guard let known = state.entries[name] else { continue }
                try? await api.deleteFile(id: known.remoteID)
                state.entries.removeValue(forKey: name)
                fileStatuses.removeValue(forKey: name)
                appendLog(.deleteRemote, name: name, "Deleted remote \(name) (removed locally)")
            }

            state.save()
            lastError = nil
            lastSyncedAt = Date()
            statusMessage = "Synced at \(Date().formatted(date: .omitted, time: .standard))"
        } catch let error as APIError where error.isUnauthorized {
            onUnauthorized()
        } catch {
            lastError = error.localizedDescription
            statusMessage = "Sync error: \(error.localizedDescription)"
            appendLog(.error, name: nil, "Sync error: \(error.localizedDescription)")
        }
    }

    /// Populates fileStatuses for every locally-known file before the diff
    /// call, so the table has a sensible badge (synced/pending) even for
    /// files that don't change this cycle. Conflict/uploading/downloading
    /// states set later in reconcile() override this baseline as they're
    /// learned.
    private func seedBaselineStatuses(localEntries: [String: LocalFileEntry]) {
        for (name, local) in localEntries {
            if let known = state.entries[name], known.size == local.size, known.sha256 == local.sha256 {
                fileStatuses[name] = .synced
            } else {
                fileStatuses[name] = .pending
            }
        }
        for name in fileStatuses.keys where localEntries[name] == nil {
            fileStatuses.removeValue(forKey: name)
        }
    }

    // MARK: - Conflict resolution (REQUIREMENTS.md §6.5)

    /// Overwrite the remote copy with what's on disk — deletes the old
    /// remote row (there is no update-in-place endpoint, see the module
    /// header) and re-uploads the local content under the same name.
    func resolveKeepLocal(_ conflict: SyncConflict) async {
        guard let folderPath else { return }
        let fileURL = URL(fileURLWithPath: folderPath).appendingPathComponent(conflict.name)
        fileStatuses[conflict.name] = .uploading
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
            finishResolving(conflict, status: .synced, logKind: .upload, message: "Kept local version of \(conflict.name)")
        } catch {
            fileStatuses[conflict.name] = .conflict
            appendLog(.error, name: conflict.name, "Failed to keep local version of \(conflict.name): \(error.localizedDescription)")
        }
    }

    /// Overwrite the local copy with the server's version, discarding the
    /// local edit that caused the conflict.
    func resolveKeepRemote(_ conflict: SyncConflict) async {
        guard let folderPath else { return }
        let fileURL = URL(fileURLWithPath: folderPath).appendingPathComponent(conflict.name)
        fileStatuses[conflict.name] = .downloading
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
            finishResolving(conflict, status: .synced, logKind: .download, message: "Kept remote version of \(conflict.name)")
        } catch {
            fileStatuses[conflict.name] = .conflict
            appendLog(.error, name: conflict.name, "Failed to keep remote version of \(conflict.name): \(error.localizedDescription)")
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
        fileStatuses[conflict.name] = .uploading

        do {
            try FileManager.default.moveItem(at: originalURL, to: copyURL)
            let uploadedCopy = try await api.uploadFile(fileURL: copyURL)
            let copyData = try Data(contentsOf: copyURL)
            state.entries[copyName] = SyncedFileEntry(
                remoteID: uploadedCopy.id, size: Int64(copyData.count),
                mtime: (try? copyURL.resourceValues(forKeys: [.contentModificationDateKey]))?
                    .contentModificationDate?.timeIntervalSince1970 ?? 0,
                sha256: FolderScanner.sha256Hex(copyData)
            )
            fileStatuses[copyName] = .synced

            fileStatuses[conflict.name] = .downloading
            let (remoteData, _) = try await api.downloadFile(id: conflict.remoteID)
            try remoteData.write(to: originalURL)
            state.entries[conflict.name] = SyncedFileEntry(
                remoteID: conflict.remoteID, size: Int64(remoteData.count),
                mtime: (try? originalURL.resourceValues(forKeys: [.contentModificationDateKey]))?
                    .contentModificationDate?.timeIntervalSince1970 ?? 0,
                sha256: FolderScanner.sha256Hex(remoteData)
            )
            state.save()
            finishResolving(conflict, status: .synced, logKind: .upload, message: "Kept both versions of \(conflict.name) — your edit is saved as \(copyName)")
        } catch {
            fileStatuses[conflict.name] = .conflict
            appendLog(.error, name: conflict.name, "Failed to keep both versions of \(conflict.name): \(error.localizedDescription)")
        }
    }

    private func finishResolving(_ conflict: SyncConflict, status: FileSyncStatus, logKind: SyncLogKind, message: String) {
        conflicts.removeAll { $0.id == conflict.id }
        fileStatuses[conflict.name] = status
        appendLog(logKind, name: conflict.name, message)
    }

    /// dateFormatter is process-local state, fine to share across calls —
    /// SyncViewModel is @MainActor, so there's no concurrent-access risk.
    private static let conflictCopyDateFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter
    }()

    static func conflictCopyName(for name: String, date: Date = Date()) -> String {
        let ext = (name as NSString).pathExtension
        let stem = (name as NSString).deletingPathExtension
        let dateStr = conflictCopyDateFormatter.string(from: date)
        return ext.isEmpty ? "\(stem) (conflict copy, \(dateStr))" : "\(stem) (conflict copy, \(dateStr)).\(ext)"
    }

    private func appendLog(_ kind: SyncLogKind, name: String?, _ message: String) {
        log.append(SyncLogEntry(kind: kind, name: name, message: message, timestamp: Date()))
        if log.count > 50 {
            log.removeFirst(log.count - 50)
        }
    }
}
