// Exercises FilesViewModel.displayedFiles (desktop/Sources/FileSpaceDesktop/FilesViewModel.swift)
// against a fake, in-memory APIClientProtocol (no network) — covering the
// assignment's specific variant from REQUIREMENTS.md §4: sorting by "Edited
// by" (ascending/descending) and filtering by extension (all / .cs / .jpg).

import XCTest
@testable import FileSpaceDesktop

/// Minimal in-memory stand-in for APIClient, just enough for
/// FilesViewModel.refresh() to populate allFiles without any network access.
private final class FakeFilesAPIClient: APIClientProtocol, @unchecked Sendable {
    var files: [FileRecord] = []

    func listFiles() async throws -> [FileRecord] { files }

    func syncDiff(lastSyncedVersion: Int64, manifest: [SyncManifestEntryDTO]) async throws -> SyncDiffResponse {
        SyncDiffResponse(newVersion: lastSyncedVersion, actions: [])
    }

    func downloadFile(id: Int64) async throws -> (data: Data, filename: String?) {
        (Data(), nil)
    }

    func fetchContent(id: Int64) async throws -> Data { Data() }

    func uploadFile(fileURL: URL) async throws -> FileRecord {
        fatalError("not used by FilesViewModelTests")
    }

    func uploadFile(fileURL: URL, onProgress: @escaping @MainActor @Sendable (Int64, Int64) -> Void) async throws -> FileRecord {
        fatalError("not used by FilesViewModelTests")
    }

    func deleteFile(id: Int64) async throws {}
}

@MainActor
final class FilesViewModelTests: XCTestCase {
    private func record(_ name: String, ext: String, editedBy: String) -> FileRecord {
        FileRecord(
            id: Int64(name.hashValue), name: name, extensionName: ext, size: 10,
            createdAt: Date(), modifiedAt: Date(), uploadedBy: "alice", editedBy: editedBy
        )
    }

    private func makeViewModel(files: [FileRecord]) async -> FilesViewModel {
        let fake = FakeFilesAPIClient()
        fake.files = files
        let vm = FilesViewModel(api: fake, onUnauthorized: {})
        await vm.refresh()
        return vm
    }

    // MARK: - Sorting by "Edited by" (REQUIREMENTS.md §4 "Sorting")

    func testSortByEditedByAscending() async {
        let vm = await makeViewModel(files: [
            record("Program.cs", ext: "cs", editedBy: "Bob"),
            record("image.jpg", ext: "jpg", editedBy: "Alice"),
        ])
        vm.toggleSort(.editedBy)

        XCTAssertEqual(vm.sortColumn, .editedBy)
        XCTAssertEqual(vm.sortDirection, .ascending)
        XCTAssertEqual(vm.displayedFiles.map(\.editedBy), ["Alice", "Bob"])
    }

    func testSortByEditedByDescending() async {
        let vm = await makeViewModel(files: [
            record("Program.cs", ext: "cs", editedBy: "Bob"),
            record("image.jpg", ext: "jpg", editedBy: "Alice"),
        ])
        vm.toggleSort(.editedBy) // -> ascending
        vm.toggleSort(.editedBy) // -> descending

        XCTAssertEqual(vm.sortDirection, .descending)
        XCTAssertEqual(vm.displayedFiles.map(\.editedBy), ["Bob", "Alice"])
    }

    func testSortByEditedByIsCaseInsensitive() async {
        let vm = await makeViewModel(files: [
            record("b.cs", ext: "cs", editedBy: "bob"),
            record("a.jpg", ext: "jpg", editedBy: "Alice"),
        ])
        vm.toggleSort(.editedBy)

        XCTAssertEqual(vm.displayedFiles.map(\.editedBy), ["Alice", "bob"])
    }

    // MARK: - Filtering by extension (REQUIREMENTS.md §4 "Filtering": all / .cs / .jpg)

    func testNoFilterShowsAllExtensions() async {
        let vm = await makeViewModel(files: [
            record("Program.cs", ext: "cs", editedBy: "Bob"),
            record("image.jpg", ext: "jpg", editedBy: "Alice"),
            record("Notes.java", ext: "java", editedBy: "Alice"),
        ])

        XCTAssertNil(vm.extensionFilter)
        XCTAssertEqual(vm.displayedFiles.count, 3)
    }

    func testFilterByCsExtensionShowsOnlyCsFiles() async {
        let vm = await makeViewModel(files: [
            record("Program.cs", ext: "cs", editedBy: "Bob"),
            record("Other.cs", ext: "cs", editedBy: "Alice"),
            record("image.jpg", ext: "jpg", editedBy: "Alice"),
        ])
        vm.extensionFilter = "cs"

        XCTAssertEqual(vm.displayedFiles.map(\.name).sorted(), ["Other.cs", "Program.cs"])
    }

    func testFilterByJpgExtensionShowsOnlyJpgFiles() async {
        let vm = await makeViewModel(files: [
            record("Program.cs", ext: "cs", editedBy: "Bob"),
            record("image.jpg", ext: "jpg", editedBy: "Alice"),
        ])
        vm.extensionFilter = "jpg"

        XCTAssertEqual(vm.displayedFiles.map(\.name), ["image.jpg"])
    }

    func testFilterAndSortByEditedByComposeTogether() async {
        let vm = await makeViewModel(files: [
            record("z.cs", ext: "cs", editedBy: "Zoe"),
            record("a.cs", ext: "cs", editedBy: "Amy"),
            record("image.jpg", ext: "jpg", editedBy: "Aaron"),
        ])
        vm.extensionFilter = "cs"
        vm.toggleSort(.editedBy)

        XCTAssertEqual(vm.displayedFiles.map(\.editedBy), ["Amy", "Zoe"])
    }
}
