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

/// POST /api/sync/diff request manifest entry (REQUIREMENTS.md §6.1/§6.6,
/// backend/internal/handler/sync.go's manifestEntry) — sending this is what
/// lets the server tell a genuine "conflict" apart from a one-sided
/// "download". Mtime is carried as an ISO8601 string purely to match the
/// wire shape; the server doesn't actually use it for conflict detection
/// (only Name and SHA256 do, see backend/internal/service/sync.go).
struct SyncManifestEntryDTO: Codable, Sendable {
    let name: String
    let size: Int64
    let mtime: String
    let sha256: String
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
    let conflict: SyncConflictDetailsDTO?

    init(name: String, action: String, remoteVersion: Int64, conflict: SyncConflictDetailsDTO? = nil) {
        self.name = name
        self.action = action
        self.remoteVersion = remoteVersion
        self.conflict = conflict
    }
}

/// Only present when a SyncActionDTO's action is "conflict" — everything
/// REQUIREMENTS.md §6.5's resolution flow needs to show the incoming
/// server-side version alongside the client's own local one, without a
/// second round trip (backend/internal/handler/sync.go's conflictDetails).
struct SyncConflictDetailsDTO: Codable, Sendable {
    let remoteSize: Int64
    let remoteModifiedAt: Date
    let remoteEditedBy: String
}

enum SyncActionKind {
    static let download = "download"
    static let deleteLocal = "delete_local"
    static let conflict = "conflict"
}
