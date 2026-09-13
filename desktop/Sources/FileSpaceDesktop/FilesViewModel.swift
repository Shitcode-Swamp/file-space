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
    @Published var editedBySortOrder: SortDirection = .none

    private let api: APIClient
    private let onUnauthorized: @MainActor () -> Void

    init(api: APIClient, onUnauthorized: @escaping @MainActor () -> Void) {
        self.api = api
        self.onUnauthorized = onUnauthorized
    }

    var availableExtensions: [String] {
        Array(Set(allFiles.map(\.extensionName))).sorted()
    }

    var displayedFiles: [FileRecord] {
        var result = allFiles
        if let extensionFilter {
            result = result.filter { $0.extensionName == extensionFilter }
        }
        switch editedBySortOrder {
        case .none:
            break
        case .ascending:
            result.sort { $0.editedBy.localizedCaseInsensitiveCompare($1.editedBy) == .orderedAscending }
        case .descending:
            result.sort { $0.editedBy.localizedCaseInsensitiveCompare($1.editedBy) == .orderedDescending }
        }
        return result
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

    func upload(fileURL: URL) async {
        errorMessage = nil
        do {
            _ = try await api.uploadFile(fileURL: fileURL)
            await refresh()
        } catch let error as APIError where error.isUnauthorized {
            onUnauthorized()
        } catch {
            errorMessage = error.localizedDescription
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
