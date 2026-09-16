// Orchestrates folder sync (REQUIREMENTS.md §5.4/§6): FSEvents-equivalent
// local watch + periodic remote poll, reconciled against the backend's
// actual capabilities.
//
// Scope boundary (matches backend/internal/service/sync.go's own documented
// boundary): POST /api/sync/diff only ever reports "download" (new/changed
// server-side) or "delete_local" (removed server-side) — the backend does
// not accept/compare a client manifest, so it can never report "upload" or
// a genuine hash-based "conflict". This client therefore:
//   - uploads a local file the first time it sees one that isn't already
//     known-synced and isn't already present remotely under that name;
//   - on a local content change to an already-synced file, deletes the old
//     remote copy and re-uploads it as a fresh row, since there is no
//     update-in-place endpoint (POST always creates, there is no PUT/PATCH);
//   - on a local deletion of an already-synced file, deletes it remotely.
// If the same file is edited locally AND changed remotely between two
// reconciles, whichever half of this function runs first wins — there is no
// true conflict detection without server-side manifest comparison. This is
// a known, documented limitation, not an oversight.
import AppKit
import Foundation

struct SyncLogLine: Identifiable {
    let id = UUID()
    let text: String
}

@MainActor
final class SyncViewModel: ObservableObject {
    @Published private(set) var folderPath: String?
    @Published private(set) var isRunning = false
    @Published private(set) var statusMessage = "Not syncing"
    @Published private(set) var log: [SyncLogLine] = []

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

    private func scheduleReconcile() {
        debounceTask?.cancel()
        debounceTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1.5))
            guard let self, !Task.isCancelled else { return }
            await self.reconcile()
        }
    }

    func reconcile() async {
        guard let folderPath else { return }
        do {
            let remoteFiles = try await api.listFiles()
            let remoteByName = Dictionary(uniqueKeysWithValues: remoteFiles.map { ($0.name, $0) })

            let diff = try await api.syncDiff(lastSyncedVersion: state.lastSyncedVersion)
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
                    appendLog("Downloaded \(action.name)")
                case SyncActionKind.deleteLocal:
                    let target = URL(fileURLWithPath: folderPath).appendingPathComponent(action.name)
                    try? FileManager.default.removeItem(at: target)
                    state.entries.removeValue(forKey: action.name)
                    appendLog("Removed local \(action.name) (deleted remotely)")
                default:
                    break
                }
            }
            state.lastSyncedVersion = diff.newVersion

            let scanResult = try FolderScanner.scan(folderPath: folderPath)
            let localEntries = scanResult.entries
            if !scanResult.unreadableNames.isEmpty {
                appendLog("Skipping \(scanResult.unreadableNames.count) unreadable file(s), left untouched: \(scanResult.unreadableNames.sorted().joined(separator: ", "))")
            }

            for (name, local) in localEntries {
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
}
