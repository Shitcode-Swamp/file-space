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
    var nextUploadID: Int64 = 1000

    func listFiles() async throws -> [FileRecord] {
        lock.withLock { remoteFiles }
    }

    /// Records the manifest each call was made with, so tests can assert on
    /// what SyncViewModel actually sent (e.g. that a conflicted file's real
    /// hash made it into the request).
    private(set) var receivedManifests: [[SyncManifestEntryDTO]] = []

    func syncDiff(lastSyncedVersion: Int64, manifest: [SyncManifestEntryDTO]) async throws -> SyncDiffResponse {
        lock.withLock {
            receivedManifests.append(manifest)
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
        fake.diffActions = [SyncActionDTO(name: "remote.txt", action: SyncActionKind.download, remoteVersion: 1, conflict: nil)]
        fake.diffNewVersion = 1
        fake.downloadContentsByName["remote.txt"] = Data("hello world".utf8)

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        let downloaded = try Data(contentsOf: tempDir.appendingPathComponent("remote.txt"))
        XCTAssertEqual(downloaded, Data("hello world".utf8))
        // The just-downloaded file is recorded as already in sync, so it
        // must not also be uploaded back.
        XCTAssertEqual(fake.uploadCallCount, 0)
    }

    // MARK: - "delete_local" action -> file removed locally

    func testDeleteLocalActionRemovesFile() async throws {
        seedSyncState()

        let fileURL = write("stale contents", named: "old.txt")
        XCTAssertTrue(FileManager.default.fileExists(atPath: fileURL.path))

        let fake = FakeAPIClient()
        fake.diffActions = [SyncActionDTO(name: "old.txt", action: SyncActionKind.deleteLocal, remoteVersion: 3, conflict: nil)]
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

    // MARK: - The manifest sent to the server reflects the local scan

    func testReconcileSendsManifestReflectingLocalScan() async throws {
        seedSyncState()
        _ = write("some content", named: "a.txt")

        let fake = FakeAPIClient()
        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        XCTAssertEqual(fake.receivedManifests.count, 1)
        let sent = fake.receivedManifests[0]
        XCTAssertEqual(sent.map(\.name), ["a.txt"])
        XCTAssertEqual(sent[0].sha256, FolderScanner.sha256Hex(Data("some content".utf8)))
    }

    // MARK: - "conflict" action -> recorded, not auto-resolved

    func testConflictActionIsRecordedAndSkipsAutoUpload() async throws {
        // Known entry whose hash no longer matches the (changed) local
        // content, exactly like testChangedKnownFileIsDeletedThenReuploaded
        // -- except this time the server reports a conflict for it instead
        // of nothing, and that must take priority over the local-change
        // auto-reupload path.
        seedSyncState(entries: [
            "budget.xlsx": SyncedFileEntry(remoteID: 55, size: 0, mtime: 0, sha256: "old-local-hash")
        ])
        _ = write("new local edit", named: "budget.xlsx")

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 55, name: "budget.xlsx", extensionName: "xlsx", size: 84000,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "tester", editedBy: "d.kim"
            )
        ]
        let remoteModified = Date(timeIntervalSince1970: 1_800_000_000)
        fake.diffActions = [
            SyncActionDTO(
                name: "budget.xlsx", action: SyncActionKind.conflict, remoteVersion: 9,
                conflict: SyncConflictDTO(remoteSize: 84000, remoteModifiedAt: remoteModified, remoteEditedBy: "d.kim")
            )
        ]
        fake.diffNewVersion = 9

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        await vm.reconcile()

        XCTAssertEqual(vm.conflicts.count, 1)
        let conflict = try XCTUnwrap(vm.conflicts.first)
        XCTAssertEqual(conflict.name, "budget.xlsx")
        XCTAssertEqual(conflict.remoteID, 55)
        XCTAssertEqual(conflict.remoteSize, 84000)
        XCTAssertEqual(conflict.remoteEditedBy, "d.kim")
        XCTAssertEqual(vm.fileStatuses["budget.xlsx"], .conflict)

        // Must NOT have taken the "known file changed locally" auto-reupload
        // path -- that would silently blow away the very conflict the user
        // is supposed to decide about.
        XCTAssertEqual(fake.uploadCallCount, 0)
        XCTAssertEqual(fake.deletedRemoteIDs, [])
    }

    // MARK: - Conflict resolution

    func testResolveKeepLocalReuploadsAndClearsConflict() async throws {
        seedSyncState(entries: ["notes.md": SyncedFileEntry(remoteID: 12, size: 0, mtime: 0, sha256: "old")])
        _ = write("my local edit", named: "notes.md")

        let fake = FakeAPIClient()
        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        let conflict = SyncConflict(
            id: "notes.md", name: "notes.md", remoteID: 12,
            localSize: 13, localModifiedAt: Date(),
            remoteSize: 999, remoteModifiedAt: Date(), remoteEditedBy: "someone"
        )

        await vm.resolveKeepLocal(conflict)

        XCTAssertEqual(fake.deletedRemoteIDs, [12])
        XCTAssertEqual(fake.uploadedContentsByName["notes.md"], Data("my local edit".utf8))
        XCTAssertTrue(vm.conflicts.isEmpty)
        XCTAssertEqual(vm.fileStatuses["notes.md"], .synced)
    }

    func testResolveKeepRemoteOverwritesLocalAndClearsConflict() async throws {
        seedSyncState(entries: ["notes.md": SyncedFileEntry(remoteID: 12, size: 0, mtime: 0, sha256: "old")])
        let fileURL = write("my local edit", named: "notes.md")

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 12, name: "notes.md", extensionName: "md", size: 20,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "tester", editedBy: "tester"
            )
        ]
        fake.downloadContentsByName["notes.md"] = Data("server's version".utf8)

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        let conflict = SyncConflict(
            id: "notes.md", name: "notes.md", remoteID: 12,
            localSize: 13, localModifiedAt: Date(),
            remoteSize: 17, remoteModifiedAt: Date(), remoteEditedBy: "someone"
        )

        await vm.resolveKeepRemote(conflict)

        let onDisk = try Data(contentsOf: fileURL)
        XCTAssertEqual(onDisk, Data("server's version".utf8))
        XCTAssertTrue(vm.conflicts.isEmpty)
        XCTAssertEqual(vm.fileStatuses["notes.md"], .synced)
        // Keep Remote discards the local edit outright -- no re-upload.
        XCTAssertEqual(fake.uploadCallCount, 0)
    }

    func testResolveKeepBothPreservesLocalEditUnderConflictCopyName() async throws {
        seedSyncState(entries: ["notes.md": SyncedFileEntry(remoteID: 12, size: 0, mtime: 0, sha256: "old")])
        let fileURL = write("my local edit", named: "notes.md")

        let fake = FakeAPIClient()
        fake.remoteFiles = [
            FileRecord(
                id: 12, name: "notes.md", extensionName: "md", size: 20,
                createdAt: Date(), modifiedAt: Date(), uploadedBy: "tester", editedBy: "tester"
            )
        ]
        fake.downloadContentsByName["notes.md"] = Data("server's version".utf8)

        let vm = SyncViewModel(api: fake, onUnauthorized: {})
        let conflict = SyncConflict(
            id: "notes.md", name: "notes.md", remoteID: 12,
            localSize: 13, localModifiedAt: Date(),
            remoteSize: 17, remoteModifiedAt: Date(), remoteEditedBy: "someone"
        )

        await vm.resolveKeepBoth(conflict)

        let copyName = SyncViewModel.conflictCopyName(for: "notes.md")
        // The original name now matches the server's content...
        let originalOnDisk = try Data(contentsOf: fileURL)
        XCTAssertEqual(originalOnDisk, Data("server's version".utf8))
        // ...and the local edit survives under the conflict-copy name, both
        // on disk and uploaded to the server.
        let copyOnDisk = try Data(contentsOf: tempDir.appendingPathComponent(copyName))
        XCTAssertEqual(copyOnDisk, Data("my local edit".utf8))
        XCTAssertEqual(fake.uploadedContentsByName[copyName], Data("my local edit".utf8))
        XCTAssertTrue(vm.conflicts.isEmpty)
    }
}
