// Drives the file list: fetches the caller's full file set once (small,
// personal-drive-sized) and does sort/filter client-side, rather than
// round-tripping to the server on every toggle the way the web client's
// TanStack Query params do — a deliberate simplification since the desktop
// client already holds the whole list in memory.

import Foundation

enum SortDirection {
    case none, ascending, descending

    var symbol: String {
        switch self {
        case .none: return "↕"
        case .ascending: return "↑"
        case .descending: return "↓"
        }
    }

    mutating func cycle() {
        switch self {
        case .none: self = .ascending
        case .ascending: self = .descending
        case .descending: self = .none
        }
    }
}

/// The columns MainView's table header lets the user click to sort by
/// (REQUIREMENTS.md §4 "Sorting" only requires "Edited by", but the same
/// click-to-sort UI applies uniformly to every column here, mirroring the
/// web client's TanStack Table sorting).
enum FileSortColumn: CaseIterable {
    case name, createdAt, modifiedAt, uploadedBy, editedBy

    var title: String {
        switch self {
        case .name: return "Name"
        case .createdAt: return "Created"
        case .modifiedAt: return "Modified"
        case .uploadedBy: return "Uploaded by"
        case .editedBy: return "Edited by"
        }
    }
}

struct UploadProgress: Identifiable {
    var id: String { name }
    let name: String
    let sent: Int64
    let total: Int64

    var fraction: Double {
        total > 0 ? Double(sent) / Double(total) : 1
    }
}

enum PreviewSupport {
    static let extensions: Set<String> = ["java", "png"]
    static func isPreviewable(_ extensionName: String) -> Bool {
        extensions.contains(extensionName.lowercased())
    }
}

enum PreviewContent {
    case text(String)
    case image(Data)
}

@MainActor
final class FilesViewModel: ObservableObject {
    @Published private(set) var allFiles: [FileRecord] = []
    @Published var isLoading = false
    @Published var errorMessage: String?
    @Published var extensionFilter: String?
    // Name, ascending, is the default sort (REQUIREMENTS.md §4); clicking a
    // column header in MainView calls toggleSort(_:), which either cycles
    // the active column's direction or switches to the clicked column at
    // ascending.
    @Published var sortColumn: FileSortColumn = .name
    @Published var sortDirection: SortDirection = .ascending
    // One entry per file in the upload currently in flight, in the same
    // order as the fileURLs passed to upload(fileURLs:) — updated as each
    // chunk finishes uploading. Empty when no upload is in progress.
    @Published private(set) var uploadProgress: [UploadProgress] = []

    private let api: APIClient
    private let onUnauthorized: @MainActor () -> Void
    private var pollTask: Task<Void, Never>?

    init(api: APIClient, onUnauthorized: @escaping @MainActor () -> Void) {
        self.api = api
        self.onUnauthorized = onUnauthorized
    }

    /// Refreshes the file list periodically in the background, so changes
    /// made elsewhere (the web client, another device, folder sync
    /// uploading/downloading files) show up without the user needing to
    /// click Refresh. Mirrors SyncViewModel's own poll loop; MainView starts
    /// this alongside the initial refresh and stops it on logout.
    func startAutoRefresh() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while let self, !Task.isCancelled {
                try? await Task.sleep(for: .seconds(15))
                guard !Task.isCancelled else { return }
                await self.refresh()
            }
        }
    }

    func stopAutoRefresh() {
        pollTask?.cancel()
        pollTask = nil
    }

    var availableExtensions: [String] {
        Array(Set(allFiles.map(\.extensionName))).sorted()
    }

    func toggleSort(_ column: FileSortColumn) {
        if sortColumn == column {
            sortDirection.cycle()
        } else {
            sortColumn = column
            sortDirection = .ascending
        }
    }

    var displayedFiles: [FileRecord] {
        var result = allFiles
        if let extensionFilter {
            result = result.filter { $0.extensionName == extensionFilter }
        }
        guard sortDirection != .none else { return result }
        let ascending = sortDirection == .ascending
        switch sortColumn {
        case .name:
            result.sort { compare($0.name, $1.name, ascending: ascending) }
        case .createdAt:
            result.sort { ascending ? $0.createdAt < $1.createdAt : $0.createdAt > $1.createdAt }
        case .modifiedAt:
            result.sort { ascending ? $0.modifiedAt < $1.modifiedAt : $0.modifiedAt > $1.modifiedAt }
        case .uploadedBy:
            result.sort { compare($0.uploadedBy, $1.uploadedBy, ascending: ascending) }
        case .editedBy:
            result.sort { compare($0.editedBy, $1.editedBy, ascending: ascending) }
        }
        return result
    }

    private func compare(_ a: String, _ b: String, ascending: Bool) -> Bool {
        let order: ComparisonResult = ascending ? .orderedAscending : .orderedDescending
        return a.localizedCaseInsensitiveCompare(b) == order
    }

    func refresh() async {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }
        do {
            allFiles = try await api.listFiles()
        } catch let error as APIError where error.isUnauthorized {
            onUnauthorized()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    /// Uploads each file in turn (sequentially, not in parallel — keeps
    /// behavior predictable and doesn't hammer the server with a burst of
    /// concurrent uploads), each split into chunks by APIClient so
    /// uploadProgress reflects real progress and large files never ride in
    /// one huge request. A failure on one file doesn't abort the rest;
    /// failures are collected and reported together once every file has
    /// been attempted.
    func upload(fileURLs: [URL]) async {
        errorMessage = nil
        var failures: [String] = []
        uploadProgress = fileURLs.map { UploadProgress(name: $0.lastPathComponent, sent: 0, total: 0) }
        for (index, url) in fileURLs.enumerated() {
            do {
                _ = try await api.uploadFile(fileURL: url) { [weak self] sent, total in
                    self?.uploadProgress[index] = UploadProgress(name: url.lastPathComponent, sent: sent, total: total)
                }
            } catch let error as APIError where error.isUnauthorized {
                uploadProgress = []
                onUnauthorized()
                return
            } catch {
                failures.append("\(url.lastPathComponent): \(error.localizedDescription)")
            }
        }
        uploadProgress = []
        await refresh()
        if !failures.isEmpty {
            errorMessage = "Some uploads failed — \(failures.joined(separator: "; "))"
        }
    }

    func download(_ file: FileRecord) async {
        errorMessage = nil
        do {
            let (data, filename) = try await api.downloadFile(id: file.id)
            let downloadsDir = FileManager.default.urls(for: .downloadsDirectory, in: .userDomainMask).first
                ?? FileManager.default.homeDirectoryForCurrentUser
            let destination = downloadsDir.appendingPathComponent(filename ?? file.name)
            try data.write(to: destination)
        } catch let error as APIError where error.isUnauthorized {
            onUnauthorized()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func delete(_ file: FileRecord) async {
        errorMessage = nil
        do {
            try await api.deleteFile(id: file.id)
            await refresh()
        } catch let error as APIError where error.isUnauthorized {
            onUnauthorized()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    /// Returns nil for non-previewable extensions without making a request,
    /// matching REQUIREMENTS.md §2 ("for all other file types, structured
    /// displaying contents is not required") rather than hitting the
    /// backend's 415 for them.
    func fetchPreview(_ file: FileRecord) async -> PreviewContent? {
        guard PreviewSupport.isPreviewable(file.extensionName) else { return nil }
        do {
            let data = try await api.fetchContent(id: file.id)
            if file.extensionName.lowercased() == "png" {
                return .image(data)
            }
            return .text(String(decoding: data, as: UTF8.self))
        } catch let error as APIError where error.isUnauthorized {
            onUnauthorized()
            return nil
        } catch {
            errorMessage = error.localizedDescription
            return nil
        }
    }
}
