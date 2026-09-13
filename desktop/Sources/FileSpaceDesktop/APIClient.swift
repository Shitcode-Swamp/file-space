// URLSession-based client for the file-space Go API (REQUIREMENTS.md §5.2).
// Mirrors frontend/src/api/client.ts's contract and retry behavior so both
// clients speak the same protocol the same way.

import Foundation

enum APIError: LocalizedError {
    case invalidResponse
    case http(status: Int, message: String)

    var errorDescription: String? {
        switch self {
        case .invalidResponse:
            return "invalid server response"
        case .http(_, let message):
            return message
        }
    }

    /// True for a 401 that survived a refresh attempt (or had none to try) —
    /// callers should treat this as "session is gone, log the user out."
    var isUnauthorized: Bool {
        if case .http(401, _) = self { return true }
        return false
    }
}

/// The subset of APIClient that SyncViewModel depends on — extracted so
/// tests can substitute an in-memory fake (no network) instead of the real
/// URLSession-backed client.
protocol APIClientProtocol: Sendable {
    func listFiles() async throws -> [FileRecord]
    func downloadFile(id: Int64) async throws -> (data: Data, filename: String?)
    func uploadFile(fileURL: URL) async throws -> FileRecord
    func deleteFile(id: Int64) async throws
    func syncDiff(lastSyncedVersion: Int64) async throws -> SyncDiffResponse
}

/// Stateless except for its own URLSession; all session state (tokens)
/// lives in KeychainStore, so this type is trivially Sendable.
final class APIClient: APIClientProtocol, Sendable {
    let baseURL: URL
    private let session: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder

    /// `session` is injectable so tests can pass a URLSession configured
    /// with a stub URLProtocol instead of hitting real network.
    init(baseURL: URL = APIClient.defaultBaseURL, session: URLSession = URLSession(configuration: .default)) {
        self.baseURL = baseURL
        self.session = session
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        self.decoder = decoder
        self.encoder = JSONEncoder()
    }

    static var defaultBaseURL: URL {
        let raw = ProcessInfo.processInfo.environment["FILESPACE_API_URL"] ?? "http://localhost:8080"
        return URL(string: raw) ?? URL(string: "http://localhost:8080")!
    }

    private func url(_ path: String) -> URL {
        baseURL.appendingPathComponent(path)
    }

    // MARK: - Low-level request helpers

    /// No auth header, no retry — used only for the auth endpoints themselves
    /// (attaching a stale token would be pointless, and retrying a failed
    /// login/refresh recursively would be wrong).
    private func sendPublic(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw APIError.invalidResponse }
        try checkStatus(data: data, http: http)
        return (data, http)
    }

    /// Attaches the stored access token; on a 401 tries exactly one refresh
    /// + retry (mirrors frontend/src/api/client.ts's behavior), then gives up.
    private func send(_ request: URLRequest, attemptRefresh: Bool = true) async throws -> (Data, HTTPURLResponse) {
        var req = request
        if let token = KeychainStore.accessToken {
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }

        let (data, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse else { throw APIError.invalidResponse }

        if http.statusCode == 401, attemptRefresh, let refreshToken = KeychainStore.refreshToken {
            if let refreshed = try? await self.refresh(refreshToken: refreshToken) {
                KeychainStore.set(refreshed.accessToken, key: "accessToken")
                return try await send(request, attemptRefresh: false)
            }
            KeychainStore.clearSession()
            throw APIError.http(status: 401, message: "session expired")
        }

        try checkStatus(data: data, http: http)
        return (data, http)
    }

    private func checkStatus(data: Data, http: HTTPURLResponse) throws {
        guard (200..<300).contains(http.statusCode) else {
            let message = (try? decoder.decode(ErrorBody.self, from: data))?.error
                ?? "request failed (\(http.statusCode))"
            throw APIError.http(status: http.statusCode, message: message)
        }
    }

    private func jsonRequest(_ path: String, method: String, body: some Encodable) throws -> URLRequest {
        var request = URLRequest(url: url(path))
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try encoder.encode(body)
        return request
    }

    // MARK: - Auth

    func register(username: String, password: String) async throws -> RegisterResponse {
        let request = try jsonRequest("api/auth/register", method: "POST", body: Credentials(username: username, password: password))
        let (data, _) = try await sendPublic(request)
        return try decoder.decode(RegisterResponse.self, from: data)
    }

    func login(username: String, password: String) async throws -> LoginResponse {
        let request = try jsonRequest("api/auth/login", method: "POST", body: Credentials(username: username, password: password))
        let (data, _) = try await sendPublic(request)
        return try decoder.decode(LoginResponse.self, from: data)
    }

    func refresh(refreshToken: String) async throws -> RefreshResponse {
        let request = try jsonRequest("api/auth/refresh", method: "POST", body: RefreshRequest(refreshToken: refreshToken))
        let (data, _) = try await sendPublic(request)
        return try decoder.decode(RefreshResponse.self, from: data)
    }

    // MARK: - Files

    /// Unfiltered/unsorted — the desktop UI fetches the full (small,
    /// personal-drive-sized) list once and filters/sorts client-side, unlike
    /// the web client's server-driven query params. See FilesViewModel.
    func listFiles() async throws -> [FileRecord] {
        let (data, _) = try await send(URLRequest(url: url("api/files")))
        return try decoder.decode([FileRecord].self, from: data)
    }

    func fetchContent(id: Int64) async throws -> Data {
        let (data, _) = try await send(URLRequest(url: url("api/files/\(id)/content")))
        return data
    }

    func downloadFile(id: Int64) async throws -> (data: Data, filename: String?) {
        let (data, http) = try await send(URLRequest(url: url("api/files/\(id)/download")))
        return (data, Self.parseFilename(from: http.value(forHTTPHeaderField: "Content-Disposition")))
    }

    func uploadFile(fileURL: URL) async throws -> FileRecord {
        let boundary = "Boundary-\(UUID().uuidString)"
        var request = URLRequest(url: url("api/files"))
        request.httpMethod = "POST"
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.httpBody = try Self.multipartBody(fileURL: fileURL, boundary: boundary)

        let (data, _) = try await send(request)
        return try decoder.decode(FileRecord.self, from: data)
    }

    func deleteFile(id: Int64) async throws {
        var request = URLRequest(url: url("api/files/\(id)"))
        request.httpMethod = "DELETE"
        _ = try await send(request)
    }

    // MARK: - Sync

    func syncDiff(lastSyncedVersion: Int64) async throws -> SyncDiffResponse {
        let request = try jsonRequest("api/sync/diff", method: "POST", body: SyncDiffRequest(lastSyncedVersion: lastSyncedVersion))
        let (data, _) = try await send(request)
        return try decoder.decode(SyncDiffResponse.self, from: data)
    }

    // MARK: - Helpers

    private struct Credentials: Encodable { let username: String; let password: String }
    private struct RefreshRequest: Encodable { let refreshToken: String }
    private struct SyncDiffRequest: Encodable { let lastSyncedVersion: Int64 }

    private static func multipartBody(fileURL: URL, boundary: String) throws -> Data {
        let fileData = try Data(contentsOf: fileURL)
        let filename = fileURL.lastPathComponent

        var body = Data()
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n".data(using: .utf8)!)
        body.append("Content-Type: application/octet-stream\r\n\r\n".data(using: .utf8)!)
        body.append(fileData)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)
        return body
    }

    private static func parseFilename(from contentDisposition: String?) -> String? {
        guard let header = contentDisposition else { return nil }
        for part in header.split(separator: ";") {
            let trimmed = part.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("filename=") {
                var value = trimmed.dropFirst("filename=".count)
                if value.hasPrefix("\"") && value.hasSuffix("\"") && value.count >= 2 {
                    value = value.dropFirst().dropLast()
                }
                return String(value)
            }
        }
        return nil
    }
}
