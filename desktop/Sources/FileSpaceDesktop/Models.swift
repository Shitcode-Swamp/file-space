// API models mirroring the Go backend's JSON shapes exactly (see
// backend/internal/handler/{auth,files,sync}.go). Kept in one file since
// they're small, plain, and have no behavior beyond (de)serialization.

import Foundation

/// GET /api/files, GET /api/files/{id}, POST /api/files response shape.
/// uploadedBy/editedBy are usernames (resolved server-side), not ids.
struct FileRecord: Codable, Identifiable, Sendable, Hashable {
    let id: Int64
    let name: String
    let extensionName: String
    let size: Int64
    let createdAt: Date
    let modifiedAt: Date
    let uploadedBy: String
    let editedBy: String

    enum CodingKeys: String, CodingKey {
        case id, name, size, createdAt, modifiedAt, uploadedBy, editedBy
        case extensionName = "extension"
    }
}

/// DD/MM/YYYY, 24-hour time, in the user's local time zone — used by
/// MainView for the Created/Modified columns. `en_US_POSIX` pins the literal
/// "dd/MM/yyyy HH:mm" pattern so it renders the same regardless of the
/// user's system locale (which would otherwise reorder the date components
/// or switch to a 12-hour clock).
let fileTimestampFormatter: DateFormatter = {
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: "en_US_POSIX")
    formatter.timeZone = .current
    formatter.dateFormat = "dd/MM/yyyy HH:mm"
    return formatter
}()

struct RegisterResponse: Codable, Sendable {
    let id: Int64
    let username: String
}

struct LoginResponse: Codable, Sendable {
    let accessToken: String
    let refreshToken: String
}

struct RefreshResponse: Codable, Sendable {
    let accessToken: String
}

struct ErrorBody: Codable, Sendable {
    let error: String
}

/// POST /api/sync/diff response shape (REQUIREMENTS.md §6.6).
struct SyncDiffResponse: Codable, Sendable {
    let newVersion: Int64
    let actions: [SyncActionDTO]
}

struct SyncActionDTO: Codable, Sendable {
    let name: String
    let action: String
    let remoteVersion: Int64
}

enum SyncActionKind {
    static let download = "download"
    static let deleteLocal = "delete_local"
}
