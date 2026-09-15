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
    func syncDiff(lastSyncedVersion: Int64, manifest: [SyncManifestEntryDTO]) async throws -> SyncDiffResponse
}

/// Stateless except for its own URLSession; all session state (tokens)
/// lives in KeychainStore, so this type is trivially Sendable.
final class APIClient: APIClientProtocol, Sendable {
    let baseURL: URL
    private let session: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder
    private let singleShotUploadThreshold: Int64

    /// `session` is injectable so tests can pass a URLSession configured
    /// with a stub URLProtocol instead of hitting real network.
    /// `singleShotUploadThreshold` is injectable so tests can force small
    /// fixture files through the chunked-upload path (by passing 0) to
    /// exercise it directly, rather than needing multi-megabyte test files
    /// to naturally clear the real default -- see uploadFile(fileURL:onProgress:).
    init(
        baseURL: URL = APIClient.defaultBaseURL,
        session: URLSession = URLSession(configuration: .default),
        singleShotUploadThreshold: Int64 = 8 << 20
    ) {
        self.baseURL = baseURL
        self.session = session
        self.singleShotUploadThreshold = singleShotUploadThreshold
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

    /// Unfiltered/unsorted, and requests the server's max page size in one
    /// call — the desktop UI fetches the full (small, personal-drive-sized)
    /// list once and filters/sorts client-side, unlike the web client's
    /// paginated, server-driven query params. See FilesViewModel. If a
    /// user's file count ever exceeds this, listFiles silently truncates to
    /// the first 1000 (by whatever order the backend defaults to); the web
    /// client's infinite scroll is the path built to handle unbounded counts.
    func listFiles() async throws -> [FileRecord] {
        var components = URLComponents(url: url("api/files"), resolvingAgainstBaseURL: false)!
        components.queryItems = [URLQueryItem(name: "limit", value: "1000")]
        let (data, _) = try await send(URLRequest(url: components.url!))
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

    /// Uploads without progress reporting — used by background sync, where
    /// there's no UI to show it to. Delegates to the chunked path below so
    /// sync uploads get the same per-request size cap as everything else.
    func uploadFile(fileURL: URL) async throws -> FileRecord {
        try await uploadFile(fileURL: fileURL, onProgress: { _, _ in })
    }

    /// Files at or below this size upload via the original single-shot
    /// Uploads fileURL in chunks (see backend/internal/service/uploads.go),
    /// invoking onProgress(bytesSent, totalBytes) after each chunk finishes.
    /// Chunking (rather than one large multipart POST, the old approach)
    /// matters in production: file-space sits behind a Cloudflare Tunnel
    /// (REQUIREMENTS.md §5.6), whose edge rejects large single-request
    /// bodies outright — every request this sends is at most one chunk,
    /// regardless of the file's total size.
    ///
    /// Files at or below singleShotUploadThreshold skip this session
    /// protocol entirely and go through uploadSingleShot instead (1 round
    /// trip vs. this path's minimum of 3: initiate + chunk + complete) --
    /// a file that already fits in a single chunk was never at risk of the
    /// Cloudflare body-size limit chunking exists for, so paying the extra
    /// round trips for it is pure overhead. That overhead is very visible
    /// for folder sync specifically: it re-uploads a changed file on every
    /// save, so this is the difference between a source-file edit
    /// resyncing in ~1 extra round trip vs. ~3.
    func uploadFile(fileURL: URL, onProgress: @escaping @MainActor @Sendable (Int64, Int64) -> Void) async throws -> FileRecord {
        let handle = try FileHandle(forReadingFrom: fileURL)
        let totalSize = try handle.seekToEnd()
        try handle.seek(toOffset: 0)

        if totalSize <= singleShotUploadThreshold {
            let data = try handle.readToEnd() ?? Data()
            try handle.close()
            let created = try await uploadSingleShot(filename: fileURL.lastPathComponent, data: data)
            await onProgress(Int64(totalSize), Int64(totalSize))
            return created
        }
        defer { try? handle.close() }

        let initiateRequest = try jsonRequest(
            "api/files/uploads", method: "POST",
            body: InitiateUploadRequest(filename: fileURL.lastPathComponent, size: Int64(totalSize))
        )
        let (initiateData, _) = try await send(initiateRequest)
        let session = try decoder.decode(InitiateUploadResponse.self, from: initiateData)

        do {
            var sent: Int64 = 0
            var index = 0
            while let chunk = try handle.read(upToCount: Int(session.chunkSize)), !chunk.isEmpty {
                var chunkRequest = URLRequest(url: url("api/files/uploads/\(session.uploadId)/chunks/\(index)"))
                chunkRequest.httpMethod = "PUT"
                chunkRequest.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
                chunkRequest.httpBody = chunk
                _ = try await send(chunkRequest)

                sent += Int64(chunk.count)
                index += 1
                await onProgress(sent, Int64(totalSize))
            }

            var completeRequest = URLRequest(url: url("api/files/uploads/\(session.uploadId)/complete"))
            completeRequest.httpMethod = "POST"
            let (completeData, _) = try await send(completeRequest)
            return try decoder.decode(FileRecord.self, from: completeData)
        } catch {
            // Best-effort: free the server-side scratch file/session now
            // rather than waiting for its idle timeout. Doesn't change what
            // gets thrown -- the upload already failed for its own reason.
            var abortRequest = URLRequest(url: url("api/files/uploads/\(session.uploadId)"))
            abortRequest.httpMethod = "DELETE"
            _ = try? await send(abortRequest)
            throw error
        }
    }

    /// The original single-shot upload path (POST /api/files,
    /// multipart/form-data) -- one round trip, used for files small enough
    /// that the chunked-upload session protocol would be pure overhead. See
    /// singleShotUploadThreshold.
    private func uploadSingleShot(filename: String, data: Data) async throws -> FileRecord {
        let boundary = "Boundary-\(UUID().uuidString)"
        var request = URLRequest(url: url("api/files"))
        request.httpMethod = "POST"
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.httpBody = Self.multipartBody(filename: filename, fileData: data, boundary: boundary)

        let (responseData, _) = try await send(request)
        return try decoder.decode(FileRecord.self, from: responseData)
    }

    private static func multipartBody(filename: String, fileData: Data, boundary: String) -> Data {
        var body = Data()
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n".data(using: .utf8)!)
        body.append("Content-Type: application/octet-stream\r\n\r\n".data(using: .utf8)!)
        body.append(fileData)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)
        return body
    }

    func deleteFile(id: Int64) async throws {
        var request = URLRequest(url: url("api/files/\(id)"))
        request.httpMethod = "DELETE"
        _ = try await send(request)
    }

    // MARK: - Sync

    func syncDiff(lastSyncedVersion: Int64, manifest: [SyncManifestEntryDTO]) async throws -> SyncDiffResponse {
        let request = try jsonRequest(
            "api/sync/diff", method: "POST",
            body: SyncDiffRequest(lastSyncedVersion: lastSyncedVersion, manifest: manifest)
        )
        let (data, _) = try await send(request)
        return try decoder.decode(SyncDiffResponse.self, from: data)
    }

    // MARK: - Helpers

    private struct Credentials: Encodable { let username: String; let password: String }
    private struct RefreshRequest: Encodable { let refreshToken: String }
    private struct SyncDiffRequest: Encodable { let lastSyncedVersion: Int64; let manifest: [SyncManifestEntryDTO] }
    private struct InitiateUploadRequest: Encodable { let filename: String; let size: Int64 }
    private struct InitiateUploadResponse: Decodable { let uploadId: String; let chunkSize: Int64 }

    private static func parseFilename(from contentDisposition: String?) -> String? {
        guard let header = contentDisposition else { return nil }
        let parts = header.split(separator: ";").map { $0.trimmingCharacters(in: .whitespaces) }

        // Prefer the RFC 6266 filename* form: it's percent-encoded UTF-8, so
        // it round-trips non-ASCII names (e.g. Ukrainian) that the server
        // also sends as an ASCII-sanitized plain filename="..." fallback.
        for part in parts where part.hasPrefix("filename*=") {
            var value = part.dropFirst("filename*=".count)
            if value.hasPrefix("UTF-8''") {
                value = value.dropFirst("UTF-8''".count)
            }
            if let decoded = String(value).removingPercentEncoding {
                return decoded
            }
        }

        for part in parts where part.hasPrefix("filename=") {
            var value = part.dropFirst("filename=".count)
            if value.hasPrefix("\"") && value.hasSuffix("\"") && value.count >= 2 {
                value = value.dropFirst().dropLast()
            }
            return String(value)
        }
        return nil
    }
}
