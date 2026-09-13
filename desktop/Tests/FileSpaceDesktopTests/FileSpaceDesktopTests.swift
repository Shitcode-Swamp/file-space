// Uses XCTest rather than the newer swift-testing `Testing` module: this
// environment only has the Xcode Command Line Tools installed (no full
// Xcode.app), which doesn't ship the Testing module for `swift test`.

import XCTest
@testable import FileSpaceDesktop

final class FileRecordDecodingTests: XCTestCase {
    /// Matches the exact JSON shape backend/internal/handler/files.go's
    /// fileResponse produces, including the "extension" key (a reserved
    /// Swift word, hence FileRecord.CodingKeys remapping it to
    /// extensionName) and RFC3339 timestamps.
    func testDecodesBackendJSONShape() throws {
        let json = """
        {
          "id": 42,
          "name": "Notes.java",
          "extension": "java",
          "size": 1234,
          "createdAt": "2026-09-12T10:00:00Z",
          "modifiedAt": "2026-09-12T11:30:00Z",
          "uploadedBy": "alice",
          "editedBy": "bob"
        }
        """
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        let file = try decoder.decode(FileRecord.self, from: Data(json.utf8))

        XCTAssertEqual(file.id, 42)
        XCTAssertEqual(file.name, "Notes.java")
        XCTAssertEqual(file.extensionName, "java")
        XCTAssertEqual(file.size, 1234)
        XCTAssertEqual(file.uploadedBy, "alice")
        XCTAssertEqual(file.editedBy, "bob")
    }

    func testDecodesFileArray() throws {
        let json = "[]"
        let decoder = JSONDecoder()
        let files = try decoder.decode([FileRecord].self, from: Data(json.utf8))
        XCTAssertTrue(files.isEmpty)
    }
}

final class SyncStateTests: XCTestCase {
    func testEntriesRoundTripThroughJSON() throws {
        var state = SyncState()
        state.folderPath = "/tmp/example"
        state.lastSyncedVersion = 7
        state.entries["a.png"] = SyncedFileEntry(remoteID: 1, size: 100, mtime: 12345, sha256: "abc")

        let data = try JSONEncoder().encode(state)
        let decoded = try JSONDecoder().decode(SyncState.self, from: data)

        XCTAssertEqual(decoded.folderPath, "/tmp/example")
        XCTAssertEqual(decoded.lastSyncedVersion, 7)
        XCTAssertEqual(decoded.entries["a.png"]?.remoteID, 1)
        XCTAssertEqual(decoded.entries["a.png"]?.sha256, "abc")
    }
}

final class FolderScannerTests: XCTestCase {
    func testSha256HexMatchesKnownVector() {
        // sha256("") — a standard test vector.
        let digest = FolderScanner.sha256Hex(Data())
        XCTAssertEqual(digest, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
    }

    func testScanSkipsHiddenFilesAndSubdirectories() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }

        try "hello".write(to: dir.appendingPathComponent("visible.txt"), atomically: true, encoding: .utf8)
        try "hidden".write(to: dir.appendingPathComponent(".hidden.txt"), atomically: true, encoding: .utf8)
        try FileManager.default.createDirectory(at: dir.appendingPathComponent("subdir"), withIntermediateDirectories: true)

        let entries = FolderScanner.scan(folderPath: dir.path)

        XCTAssertEqual(entries.count, 1)
        XCTAssertNotNil(entries["visible.txt"])
        XCTAssertNil(entries[".hidden.txt"])
        XCTAssertNil(entries["subdir"])
    }
}

final class PreviewSupportTests: XCTestCase {
    func testOnlyJavaAndPngArePreviewable() {
        XCTAssertTrue(PreviewSupport.isPreviewable("java"))
        XCTAssertTrue(PreviewSupport.isPreviewable("PNG"))
        XCTAssertFalse(PreviewSupport.isPreviewable("pdf"))
        XCTAssertFalse(PreviewSupport.isPreviewable(""))
    }
}
