// Exercises SyncViewModel.reconcile() (desktop/Sources/FileSpaceDesktop/SyncViewModel.swift)
// against a fake, in-memory APIClientProtocol (no network) and a real temp
// directory standing in for the synced folder.
//
// Caveat: SyncViewModel.init() unconditionally calls the real
// `SyncState.load()`, which reads a JSON sidecar from a fixed path under
// ~/Library/Application Support/FileSpaceDesktop/sync-state.json — there is
// no injection point for this (SyncState/SyncedFileEntry are plain
// Codable structs with no seam added in Part 1, per the task's own
// guidance to prefer constructing them directly over adding more seams).
// So, exactly like KeychainStoreTests avoids colliding with a real app
// instance, these tests back up whatever is really at that sidecar path
// before each test, write a crafted SyncState there (via its own, already
// internal, `.save()` method) so SyncViewModel.init() picks it up, and
// restore the original bytes (or remove the file if it didn't exist before)
// in tearDown.
//
// This is the part of the suite I'm least able to fully verify by reading
// alone, since it depends on: (a) FolderScanner's hidden-file-skipping/hash
// behavior interacting correctly with freshly-written test files, and
// (b) the exact sidecar path replication below staying byte-for-byte
// identical to SyncState.fileURL's private implementation. I re-read both
// closely and believe they match, but flagging the assumption rather than
// asserting confidence I can't actually compile-check.

import XCTest
@testable import FileSpaceDesktop

/// In-memory, no-network stand-in for APIClient. All mutable state is
/// guarded by a lock since APIClientProtocol requires Sendable and
/// SyncViewModel awaits these methods from the main actor.
final class FakeAPIClient: APIClientProtocol, @unchecked Sendable {
    private let lock = NSLock()

    // Canned responses, set up by each test before calling reconcile().
    var remoteFiles: [FileRecord] = []
    var diffActions: [SyncActionDTO] = []
    var diffNewVersion: Int64 = 0
    var downloadContentsByName: [String: Data] = [:]

    // Recorded calls, inspected by each test afterward.
    private(set) var uploadedContentsByName: [String: Data] = [:]
    private(set) var uploadCallCount = 0
    private(set) var deletedRemoteIDs: [Int64] = []
    private(set) var lastManifestSent: [SyncManifestEntryDTO]?
    private(set) var listFilesCallCount = 0
    var nextUploadID: Int64 = 1000
    /// Artificial delay before listFiles() returns, letting a test hold one
    /// reconcile() call "in flight" long enough to start a second one and
    /// observe whether it's allowed to run concurrently.
    var listFilesDelay: Duration = .zero

    func listFiles() async throws -> [FileRecord] {
        lock.withLock { listFilesCallCount += 1 }
        if listFilesDelay > .zero {
            try? await Task.sleep(for: listFilesDelay)
        }
        return lock.withLock { remoteFiles }
    }

    func syncDiff(lastSyncedVersion: Int64, manifest: [SyncManifestEntryDTO]) async throws -> SyncDiffResponse {
        lock.withLock {
            lastManifestSent = manifest
            return SyncDiffResponse(newVersion: diffNewVersion, actions: diffActions)
        }
    }

    func downloadFile(id: Int64) async throws -> (data: Data, filename: String?) {
        lock.withLock {
            let name = remoteFiles.first(where: { $0.id == id })?.name
            let data = name.flatMap { downloadContentsByName[$0] } ?? Data()
            return (data, name)
        }
    }

    func uploadFile(fileURL: URL) async throws -> FileRecord {
        let name = fileURL.lastPathComponent
        let data = (try? Data(contentsOf: fileURL)) ?? Data()
        let id = lock.withLock { () -> Int64 in
            uploadedContentsByName[name] = data
            uploadCallCount += 1
            let assignedID = nextUploadID
            nextUploadID += 1
            return assignedID
        }
        return FileRecord(
            id: id,
            name: name,
            extensionName: (name as NSString).pathExtension,
            size: Int64(data.count),
            createdAt: Date(),
            modifiedAt: Date(),
            uploadedBy: "tester",
            editedBy: "tester"
        )
    }

    func uploadFile(fileURL: URL, onProgress: @escaping @MainActor @Sendable (Int64, Int64) -> Void) async throws -> FileRecord {
        try await uploadFile(fileURL: fileURL)
    }

    func fetchContent(id: Int64) async throws -> Data {
        lock.withLock {
            let name = remoteFiles.first(where: { $0.id == id })?.name
            return name.flatMap { downloadContentsByName[$0] } ?? Data()
        }
    }

    func deleteFile(id: Int64) async throws {
        lock.withLock { deletedRemoteIDs.append(id) }
    }
}

@MainActor
final class SyncViewModelTests: XCTestCase {
    private var savedSidecarData: Data?
    private var tempDir: URL!

    /// Mirrors SyncState.fileURL's (private) path computation exactly —
    /// duplicated here only because there's no accessor for it, not because
    /// it's expected to drift independently from the production code.
    private var sidecarURL: URL {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("FileSpaceDesktop", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("sync-state.json")
    }

    override func setUp() {
        super.setUp()
        savedSidecarData = try? Data(contentsOf: sidecarURL)
        tempDir = FileManager.default.temporaryDirectory
            .appendingPathComponent("SyncViewModelTests-\(UUID().uuidString)", isDirectory: true)
        try? FileManager.default.createDirectory(at: tempDir, withIntermediateDirectories: true)
    }

    override func tearDown() {
        if let savedSidecarData {
            try? savedSidecarData.write(to: sidecarURL)
        } else {
            try? FileManager.default.removeItem(at: sidecarURL)
        }
        try? FileManager.default.removeItem(at: tempDir)
        tempDir = nil
        super.tearDown()
    }

    /// Writes a crafted SyncState to the real sidecar path so the next
    /// SyncViewModel we construct loads exactly this state.
    private func seedSyncState(lastSyncedVersion: Int64 = 0, entries: [String: SyncedFileEntry] = [:]) {
        var state = SyncState()
        state.folderPath = tempDir.path
        state.lastSyncedVersion = lastSyncedVersion
        state.entries = entries
        state.save()
    }

    private func write(_ contents: String, named name: String) -> URL {
        let url = tempDir.appendingPathComponent(name)
        try! Data(contents.utf8).write(to: url)
        return url
    }

    // MARK: - New local file -> uploaded

    func testNewLocalFileGetsUploaded() async throws {
        seedSyncState() // no known entries, nothing remote

        _ = write("brand new contents", named: "new.txt")

        let fake = FakeAPIClient()
        // remoteFiles left empty: the file isn't known remotely either.

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        XCTAssertEqual(fake.uploadCallCount, 1)
        XCTAssertEqual(fake.uploadedContentsByName["new.txt"], Data("brand new contents".utf8))
        XCTAssertEqual(fake.deletedRemoteIDs, [])
    }

    // MARK: - "download" action -> file written locally with correct bytes

    func testDownloadActionWritesFileWithCorrectBytes() async throws {
        seedSyncState() // no known entries yet

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 7, name: "remote.txt", extensionName: "txt", size: 11,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "alice", editedBy: "alice"
            )
        ]
        fake.diffActions = [SyncActionDTO(name: "remote.txt", action: SyncActionKind.download, remoteVersion: 1)]
        fake.diffNewVersion = 1
        fake.downloadContentsByName["remote.txt"] = Data("hello world".utf8)

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        let downloaded = try Data(contentsOf: tempDir.appendingPathComponent("remote.txt"))
        XCTAssertEqual(downloaded, Data("hello world".utf8))
        // The just-downloaded file is recorded as already in sync, so it
        // must not also be uploaded back.
        XCTAssertEqual(fake.uploadCallCount, 0)
        // Regression: the pre-cycle folder scan doesn't include a file this
        // *same* cycle just downloaded, so the "what's missing locally"
        // check must not read that gap as "the user deleted it" and delete
        // it right back off the server.
        XCTAssertEqual(fake.deletedRemoteIDs, [])
    }

    /// A file downloaded this cycle must survive the same cycle's
    /// removedLocally pass even when there's *another*, unrelated,
    /// genuinely-removed local file to process alongside it -- guarding
    /// against a fix that special-cases "the only entry" instead of
    /// correctly excluding just the downloaded name.
    func testDownloadedFileSurvivesAlongsideAGenuineLocalRemoval() async throws {
        seedSyncState(entries: [
            "goneLocally.txt": SyncedFileEntry(remoteID: 55, size: 3, mtime: 0, sha256: "old")
        ])
        // "goneLocally.txt" is deliberately not written to tempDir -- it was
        // removed from the folder since the last sync.

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 7, name: "remote.txt", extensionName: "txt", size: 11,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "alice", editedBy: "alice"
            )
        ]
        fake.diffActions = [SyncActionDTO(name: "remote.txt", action: SyncActionKind.download, remoteVersion: 1)]
        fake.diffNewVersion = 1
        fake.downloadContentsByName["remote.txt"] = Data("hello world".utf8)

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        XCTAssertTrue(FileManager.default.fileExists(atPath: tempDir.appendingPathComponent("remote.txt").path))
        // Only the genuinely-removed file gets deleted remotely.
        XCTAssertEqual(fake.deletedRemoteIDs, [55])
    }

    // MARK: - "delete_local" action -> file removed locally

    func testDeleteLocalActionRemovesFile() async throws {
        seedSyncState()

        let fileURL = write("stale contents", named: "old.txt")
        XCTAssertTrue(FileManager.default.fileExists(atPath: fileURL.path))

        let fake = FakeAPIClient()
        fake.diffActions = [SyncActionDTO(name: "old.txt", action: SyncActionKind.deleteLocal, remoteVersion: 3)]
        fake.diffNewVersion = 3

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        XCTAssertFalse(FileManager.default.fileExists(atPath: fileURL.path))
        // delete_local is a server-driven local removal, not a remote delete.
        XCTAssertEqual(fake.deletedRemoteIDs, [])
        XCTAssertEqual(fake.uploadCallCount, 0)
    }

    // MARK: - Known-synced file whose content changed -> delete then re-upload

    func testChangedKnownFileIsDeletedThenReuploaded() async throws {
        // Known entry deliberately has size 0, which cannot match any
        // non-empty file we write below, so the "content changed" branch
        // always triggers regardless of the new content's actual hash.
        seedSyncState(entries: [
            "changed.txt": SyncedFileEntry(remoteID: 55, size: 0, mtime: 0, sha256: "old-hash")
        ])

        _ = write("new content here", named: "changed.txt")

        let fake = FakeAPIClient()
        // Remote listing can (and typically would) still show the old row;
        // the reupload branch is driven by the known-entries comparison,
        // not by remoteByName.
        fake.remoteFiles = [
            FileRecord(
                id: 55, name: "changed.txt", extensionName: "txt", size: 0,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "tester", editedBy: "tester"
            )
        ]

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        XCTAssertEqual(fake.deletedRemoteIDs, [55])
        XCTAssertEqual(fake.uploadCallCount, 1)
        XCTAssertEqual(fake.uploadedContentsByName["changed.txt"], Data("new content here".utf8))
    }

    // MARK: - Known-synced file deleted by the user -> remote delete

    func testLocallyDeletedKnownFileTriggersRemoteDelete() async throws {
        seedSyncState(entries: [
            "gone.txt": SyncedFileEntry(remoteID: 77, size: 10, mtime: 0, sha256: "whatever")
        ])
        // Deliberately do not create gone.txt in tempDir: the user deleted it.

        let fake = FakeAPIClient()

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        XCTAssertEqual(fake.deletedRemoteIDs, [77])
        XCTAssertEqual(fake.uploadCallCount, 0)
    }

    // MARK: - Conflict detection (backend/internal/service/sync.go's
    // hash-based "conflict" action, REQUIREMENTS.md §6.5) — "my variant" of
    // the sync operation: the desktop client's half of that feature.

    /// reconcile() must submit the locally-scanned manifest (name/size/hash
    /// per file) with every syncDiff call — that manifest is exactly what
    /// lets the server tell a genuine conflict apart from a one-sided
    /// download (see backend/internal/service/sync.go's Diff).
    func testReconcileSendsLocalManifestWithHashes() async throws {
        seedSyncState()
        let contents = "manifest me"
        _ = write(contents, named: "a.txt")

        let fake = FakeAPIClient()
        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        let manifest = try XCTUnwrap(fake.lastManifestSent)
        let entry = try XCTUnwrap(manifest.first(where: { $0.name == "a.txt" }))
        XCTAssertEqual(entry.size, Int64(contents.utf8.count))
        XCTAssertEqual(entry.sha256, FolderScanner.sha256Hex(Data(contents.utf8)))
    }

    /// A "conflict" action must not be auto-resolved: no upload/delete for
    /// that file this cycle, and it must show up in viewModel.conflicts with
    /// the server's remote-side details attached.
    func testConflictActionIsRecordedAndSkipsAutoUpload() async throws {
        seedSyncState(entries: [
            "shared.txt": SyncedFileEntry(remoteID: 42, size: 0, mtime: 0, sha256: "old-hash")
        ])
        _ = write("locally edited content", named: "shared.txt")

        let remoteModifiedAt = Date(timeIntervalSince1970: 1_700_000_000)
        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 42, name: "shared.txt", extensionName: "txt", size: 99,
                createdAt: Date(), modifiedAt: remoteModifiedAt, uploadedBy: "bob", editedBy: "bob"
            )
        ]
        fake.diffActions = [
            SyncActionDTO(
                name: "shared.txt", action: SyncActionKind.conflict, remoteVersion: 5,
                conflict: SyncConflictDetailsDTO(remoteSize: 99, remoteModifiedAt: remoteModifiedAt, remoteEditedBy: "bob")
            )
        ]
        fake.diffNewVersion = 5

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        XCTAssertEqual(fake.uploadCallCount, 0, "a conflicted file must not be auto-reuploaded")
        XCTAssertEqual(fake.deletedRemoteIDs, [], "a conflicted file must not be auto-deleted remotely")
        XCTAssertEqual(vm.conflicts.count, 1)
        let conflict = try XCTUnwrap(vm.conflicts.first)
        XCTAssertEqual(conflict.name, "shared.txt")
        XCTAssertEqual(conflict.remoteID, 42)
        XCTAssertEqual(conflict.remoteSize, 99)
        XCTAssertEqual(conflict.remoteEditedBy, "bob")
        XCTAssertEqual(conflict.localSize, Int64("locally edited content".utf8.count))
    }

    /// "Keep Local": remote row is deleted and replaced with what's on disk;
    /// the conflict is cleared afterward.
    func testResolveKeepLocalReuploadsAndClearsConflict() async throws {
        seedSyncState(entries: [
            "shared.txt": SyncedFileEntry(remoteID: 42, size: 0, mtime: 0, sha256: "old-hash")
        ])
        _ = write("keep the local edit", named: "shared.txt")

        let fake = FakeAPIClient()
        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        let conflict = SyncConflict(
            name: "shared.txt", remoteID: 42, localSize: 20, localModifiedAt: Date(),
            remoteSize: 99, remoteModifiedAt: Date(), remoteEditedBy: "bob"
        )

        await vm.resolveKeepLocal(conflict)

        XCTAssertEqual(fake.deletedRemoteIDs, [42])
        XCTAssertEqual(fake.uploadCallCount, 1)
        XCTAssertEqual(fake.uploadedContentsByName["shared.txt"], Data("keep the local edit".utf8))
        XCTAssertTrue(vm.conflicts.isEmpty)
    }

    /// "Keep Remote": local file is overwritten with the server's content,
    /// discarding the local edit; nothing is (re-)uploaded.
    func testResolveKeepRemoteOverwritesLocalFile() async throws {
        seedSyncState(entries: [
            "shared.txt": SyncedFileEntry(remoteID: 42, size: 0, mtime: 0, sha256: "old-hash")
        ])
        let fileURL = write("stale local edit", named: "shared.txt")

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 42, name: "shared.txt", extensionName: "txt", size: 12,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "bob", editedBy: "bob"
            )
        ]
        fake.downloadContentsByName["shared.txt"] = Data("server content".utf8)

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        let conflict = SyncConflict(
            name: "shared.txt", remoteID: 42, localSize: 17, localModifiedAt: Date(),
            remoteSize: 14, remoteModifiedAt: Date(), remoteEditedBy: "bob"
        )

        await vm.resolveKeepRemote(conflict)

        let onDisk = try Data(contentsOf: fileURL)
        XCTAssertEqual(onDisk, Data("server content".utf8))
        XCTAssertEqual(fake.uploadCallCount, 0)
        XCTAssertTrue(vm.conflicts.isEmpty)
    }

    /// "Keep Both": the local edit survives as a renamed "conflict copy" and
    /// gets uploaded as a new file, while the original name ends up holding
    /// the server's content on disk.
    func testResolveKeepBothPreservesLocalCopyAndAdoptsRemote() async throws {
        seedSyncState(entries: [
            "shared.txt": SyncedFileEntry(remoteID: 42, size: 0, mtime: 0, sha256: "old-hash")
        ])
        let fileURL = write("my local edit", named: "shared.txt")

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 42, name: "shared.txt", extensionName: "txt", size: 12,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "bob", editedBy: "bob"
            )
        ]
        fake.downloadContentsByName["shared.txt"] = Data("server content".utf8)

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        let conflict = SyncConflict(
            name: "shared.txt", remoteID: 42, localSize: 13, localModifiedAt: Date(),
            remoteSize: 14, remoteModifiedAt: Date(), remoteEditedBy: "bob"
        )

        await vm.resolveKeepBoth(conflict)

        let copyName = SyncViewModel.conflictCopyName(for: "shared.txt")
        let copyURL = tempDir.appendingPathComponent(copyName)
        XCTAssertEqual(try Data(contentsOf: copyURL), Data("my local edit".utf8))
        XCTAssertEqual(fake.uploadedContentsByName[copyName], Data("my local edit".utf8))
        XCTAssertEqual(try Data(contentsOf: fileURL), Data("server content".utf8))
        XCTAssertTrue(vm.conflicts.isEmpty)
    }

    func testConflictCopyNamePreservesExtensionAndInsertsDate() {
        var components = DateComponents()
        components.year = 2026; components.month = 9; components.day = 11
        let fixedDate = Calendar(identifier: .gregorian).date(from: components)!

        XCTAssertEqual(
            SyncViewModel.conflictCopyName(for: "Program.cs", today: fixedDate),
            "Program (conflict copy, 2026-09-11).cs"
        )
        XCTAssertEqual(
            SyncViewModel.conflictCopyName(for: "README", today: fixedDate),
            "README (conflict copy, 2026-09-11)"
        )
    }

    // MARK: - selectFolder(path:) resets stale bookkeeping across folders
    //
    // Regression coverage for a real bug: the sync sidecar is a single file
    // keyed only by filename, not per-folder. Pointing sync at a *different*
    // folder without clearing lastSyncedVersion/entries made reconcile()
    // think the new folder already had whatever files were known-synced from
    // the old one -- which, combined with the "file missing locally -> was
    // deleted -> delete it remotely too" logic, deleted the user's remote
    // files just for choosing an empty folder.

    func testSelectingADifferentFolderResetsStaleSyncState() async throws {
        // Old folder: "known.txt" was already synced as remote id 99.
        seedSyncState(lastSyncedVersion: 42, entries: [
            "known.txt": SyncedFileEntry(remoteID: 99, size: 5, mtime: 0, sha256: "irrelevant")
        ])

        let newFolder = FileManager.default.temporaryDirectory
            .appendingPathComponent("SyncViewModelTests-other-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: newFolder, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: newFolder) }

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 99, name: "known.txt", extensionName: "txt", size: 5,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "tester", editedBy: "tester"
            )
        ]
        fake.diffActions = [] // nothing changed server-side since version 42

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        vm.selectFolder(path: newFolder.path) // a different, empty folder
        await vm.reconcile()

        // Before the fix: "known.txt" (absent from the new, empty folder)
        // would be read as "deleted locally" and its remote copy destroyed.
        XCTAssertEqual(fake.deletedRemoteIDs, [], "switching folders must not delete files that only exist in the old folder's bookkeeping")
        XCTAssertEqual(fake.uploadCallCount, 0)
    }

    func testReselectingTheSameFolderPreservesSyncState() async throws {
        let contents = "same content"
        seedSyncState(entries: [
            "known.txt": SyncedFileEntry(remoteID: 99, size: Int64(contents.utf8.count), mtime: 0, sha256: FolderScanner.sha256Hex(Data(contents.utf8)))
        ])
        _ = write(contents, named: "known.txt")

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 99, name: "known.txt", extensionName: "txt", size: 12,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "tester", editedBy: "tester"
            )
        ]

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        vm.selectFolder(path: tempDir.path) // re-selecting the same folder

        await vm.reconcile()

        // The known entry survived, its hash matches the unchanged file, so
        // it's neither re-uploaded nor deleted.
        XCTAssertEqual(fake.uploadCallCount, 0)
        XCTAssertEqual(fake.deletedRemoteIDs, [])
    }

    // MARK: - reconcile() refuses to run concurrently with itself
    //
    // Regression coverage for a real bug: the 45s poll loop and a debounced
    // FSEvents callback each call reconcile() from their own Task chain,
    // with no mutual exclusion. Since reconcile() is full of await points,
    // the actor can freely interleave a second call while the first is
    // still suspended on network I/O -- and because both calls read/mutate
    // the same `state` property from their own, independently-captured
    // folder scans, a file the *other* call just uploaded looked, from a
    // stale scan's perspective, like a file that used to be known but is
    // now missing locally, and got deleted remotely for it (which then came
    // back as a real delete_local action, deleting it locally too).

    func testReconcileDoesNotRunConcurrentlyWithItself() async throws {
        seedSyncState()
        let fake = FakeAPIClient()
        fake.listFilesDelay = .milliseconds(200) // holds the first call "in flight"

        let vm = SyncViewModel(api: fake, onUnauthorized: {})

        let firstCall = Task { await vm.reconcile() }
        try await Task.sleep(for: .milliseconds(50)) // let the first call actually start

        // A second call arriving while the first is still in flight (e.g.
        // from a debounced FSEvents callback firing mid-poll) must be a
        // no-op, not a second concurrent pass over `state`.
        await vm.reconcile()
        XCTAssertEqual(fake.listFilesCallCount, 1, "a reconcile already in flight must block a concurrent second call rather than let it run")

        await firstCall.value
        XCTAssertEqual(fake.listFilesCallCount, 1, "the blocked call must not have run afterward either -- it was skipped, not queued")
    }
}
