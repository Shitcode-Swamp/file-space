// Exercises APIClient's request/refresh/retry behavior (see
// desktop/Sources/FileSpaceDesktop/APIClient.swift's `send(_:attemptRefresh:)`)
// against a stub URLProtocol instead of real network, using the injectable
// `session` initializer parameter added for testability in Part 1.
//
// Caveat: APIClient reads/writes the *real* Keychain via the hardcoded
// "accessToken" / "refreshToken" / "username" keys (KeychainStore.accessToken
// etc. are not injectable — that's baked into APIClient itself, not part of
// the seam this task added). So these tests save whatever is really stored
// under those keys before running and restore it afterward, the same way
// KeychainStoreTests avoids colliding with a real running app instance.

import XCTest
@testable import FileSpaceDesktop

/// Thread-safe call counter / request log, since URLProtocol's methods can be
/// invoked off the main thread by URLSession's internal loading machinery.
final class CallRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var counts: [String: Int] = [:]

    func record(_ key: String) {
        lock.lock()
        defer { lock.unlock() }
        counts[key, default: 0] += 1
    }

    func count(_ key: String) -> Int {
        lock.lock()
        defer { lock.unlock() }
        return counts[key] ?? 0
    }
}

struct StubResponse {
    let statusCode: Int
    let headers: [String: String]
    let body: Data

    static func json(_ statusCode: Int, _ object: Any, headers: [String: String] = [:]) -> StubResponse {
        let body = (try? JSONSerialization.data(withJSONObject: object)) ?? Data()
        var allHeaders = headers
        allHeaders["Content-Type"] = "application/json"
        return StubResponse(statusCode: statusCode, headers: allHeaders, body: body)
    }
}

/// Intercepts every request made through a session configured with it and
/// hands it to a test-supplied handler closure instead of hitting the network.
final class StubURLProtocol: URLProtocol, @unchecked Sendable {
    typealias Handler = @Sendable (URLRequest) -> StubResponse

    private static let lock = NSLock()
    nonisolated(unsafe) private static var _handler: Handler?

    static var handler: Handler? {
        get { lock.lock(); defer { lock.unlock() }; return _handler }
        set { lock.lock(); defer { lock.unlock() }; _handler = newValue }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let handler = StubURLProtocol.handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.unsupportedURL))
            return
        }
        let stub = handler(request)
        let response = HTTPURLResponse(
            url: request.url ?? URL(string: "http://localhost:8080")!,
            statusCode: stub.statusCode,
            httpVersion: "HTTP/1.1",
            headerFields: stub.headers
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: stub.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

final class APIClientTests: XCTestCase {
    private var savedAccessToken: String?
    private var savedRefreshToken: String?
    private var savedUsername: String?

    override func setUp() {
        super.setUp()
        savedAccessToken = KeychainStore.accessToken
        savedRefreshToken = KeychainStore.refreshToken
        savedUsername = KeychainStore.username
        StubURLProtocol.handler = nil
    }

    override func tearDown() {
        if let savedAccessToken {
            KeychainStore.set(savedAccessToken, key: "accessToken")
        } else {
            KeychainStore.delete("accessToken")
        }
        if let savedRefreshToken {
            KeychainStore.set(savedRefreshToken, key: "refreshToken")
        } else {
            KeychainStore.delete("refreshToken")
        }
        if let savedUsername {
            KeychainStore.set(savedUsername, key: "username")
        } else {
            KeychainStore.delete("username")
        }
        StubURLProtocol.handler = nil
        super.tearDown()
    }

    /// `singleShotUploadThreshold` defaults to the real 8MiB cutoff;
    /// chunked-upload tests pass 0 so their small fixture files still
    /// exercise the chunked session protocol instead of legitimately
    /// qualifying for the single-shot fast path.
    private func makeClient(singleShotUploadThreshold: Int64 = 8 << 20) -> APIClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        let session = URLSession(configuration: config)
        return APIClient(
            baseURL: URL(string: "http://stub.invalid")!,
            session: session,
            singleShotUploadThreshold: singleShotUploadThreshold
        )
    }

    func testFilesRequestRetriesExactlyOnceAfterSuccessfulRefresh() async throws {
        KeychainStore.set("expired-token", key: "accessToken")
        KeychainStore.set("valid-refresh-token", key: "refreshToken")

        let recorder = CallRecorder()
        let filesCallCount = CallRecorder()

        StubURLProtocol.handler = { request in
            let path = request.url?.path ?? ""
            if path.contains("auth/refresh") {
                recorder.record("refresh")
                return .json(200, ["accessToken": "new-token"])
            }
            if path.contains("api/files") {
                filesCallCount.record("files")
                if filesCallCount.count("files") == 1 {
                    // First attempt: stale token, unauthorized.
                    return StubResponse(statusCode: 401, headers: [:], body: Data("{\"error\":\"unauthorized\"}".utf8))
                }
                // Retry after refresh succeeds.
                return .json(200, [] as [Any])
            }
            return StubResponse(statusCode: 404, headers: [:], body: Data())
        }

        let client = makeClient()
        let files = try await client.listFiles()

        XCTAssertEqual(files.count, 0)
        XCTAssertEqual(recorder.count("refresh"), 1, "refresh should be attempted exactly once")
        XCTAssertEqual(filesCallCount.count("files"), 2, "the original request plus exactly one retry")
    }

    func testFilesRequestThrowsAndClearsTokensWhenRefreshAlsoFails() async throws {
        KeychainStore.set("expired-token", key: "accessToken")
        KeychainStore.set("bad-refresh-token", key: "refreshToken")
        KeychainStore.set("someone", key: "username")

        StubURLProtocol.handler = { request in
            let path = request.url?.path ?? ""
            if path.contains("auth/refresh") {
                return StubResponse(statusCode: 401, headers: [:], body: Data("{\"error\":\"invalid refresh token\"}".utf8))
            }
            if path.contains("api/files") {
                return StubResponse(statusCode: 401, headers: [:], body: Data("{\"error\":\"unauthorized\"}".utf8))
            }
            return StubResponse(statusCode: 404, headers: [:], body: Data())
        }

        let client = makeClient()

        do {
            _ = try await client.listFiles()
            XCTFail("expected the call to throw once refresh also fails")
        } catch let error as APIError {
            XCTAssertTrue(error.isUnauthorized)
        }

        XCTAssertNil(KeychainStore.accessToken, "accessToken should be cleared after a failed refresh")
        XCTAssertNil(KeychainStore.refreshToken, "refreshToken should be cleared after a failed refresh")
        XCTAssertNil(KeychainStore.username, "username should be cleared after a failed refresh")
    }

    func testSuccessfulResponseDecodesIntoExpectedModel() async throws {
        KeychainStore.set("valid-token", key: "accessToken")
        KeychainStore.set("valid-refresh-token", key: "refreshToken")

        StubURLProtocol.handler = { _ in
            return .json(200, [
                [
                    "id": 42,
                    "name": "Notes.java",
                    "extension": "java",
                    "size": 1234,
                    "createdAt": "2026-09-12T10:00:00Z",
                    "modifiedAt": "2026-09-12T11:30:00Z",
                    "uploadedBy": "alice",
                    "editedBy": "bob",
                ] as [String: Any]
            ])
        }

        let client = makeClient()
        let files = try await client.listFiles()

        XCTAssertEqual(files.count, 1)
        XCTAssertEqual(files[0].id, 42)
        XCTAssertEqual(files[0].name, "Notes.java")
        XCTAssertEqual(files[0].extensionName, "java")
        XCTAssertEqual(files[0].size, 1234)
        XCTAssertEqual(files[0].uploadedBy, "alice")
        XCTAssertEqual(files[0].editedBy, "bob")
    }

    private func makeTempFile(bytes: Int) throws -> URL {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + "-big.bin")
        try Data((0..<bytes).map { UInt8($0 % 256) }).write(to: url)
        return url
    }

    func testUploadFileInChunksSendsInOrderAndReportsProgress() async throws {
        KeychainStore.set("valid-token", key: "accessToken")

        let fileURL = try makeTempFile(bytes: 10)
        defer { try? FileManager.default.removeItem(at: fileURL) }

        let requests = RequestLog()
        StubURLProtocol.handler = { request in
            let path = request.url?.path ?? ""
            let method = request.httpMethod ?? ""
            requests.record(method: method, path: path)

            if path == "/api/files/uploads" && method == "POST" {
                return .json(201, ["uploadId": "up-1", "chunkSize": 4])
            }
            if path.hasPrefix("/api/files/uploads/up-1/chunks/") && method == "PUT" {
                return StubResponse(statusCode: 204, headers: [:], body: Data())
            }
            if path == "/api/files/uploads/up-1/complete" && method == "POST" {
                return .json(201, [
                    "id": 7, "name": "big.bin", "extension": "bin", "size": 10,
                    "createdAt": "2026-09-12T10:00:00Z", "modifiedAt": "2026-09-12T10:00:00Z",
                    "uploadedBy": "alice", "editedBy": "alice",
                ] as [String: Any])
            }
            return StubResponse(statusCode: 404, headers: [:], body: Data())
        }

        let client = makeClient(singleShotUploadThreshold: 0)
        let progress = ProgressLog()
        let created = try await client.uploadFile(fileURL: fileURL) { sent, total in
            progress.record(sent: sent, total: total)
        }

        XCTAssertEqual(created.id, 7)
        XCTAssertEqual(created.name, "big.bin")

        // 10 bytes at chunkSize 4 -> chunks of 4, 4, 2 bytes, in order.
        let chunkPaths = requests.entries.filter { $0.method == "PUT" }.map(\.path)
        XCTAssertEqual(chunkPaths, [
            "/api/files/uploads/up-1/chunks/0",
            "/api/files/uploads/up-1/chunks/1",
            "/api/files/uploads/up-1/chunks/2",
        ])
        XCTAssertEqual(progress.entries.map(\.sent), [4, 8, 10])
        XCTAssertEqual(progress.entries.map(\.total), [10, 10, 10])

        // initiate, 3 chunks, complete.
        XCTAssertEqual(requests.entries.count, 5)
        XCTAssertEqual(requests.entries.last?.method, "POST")
        XCTAssertEqual(requests.entries.last?.path, "/api/files/uploads/up-1/complete")
    }

    func testUploadFileInChunksAbortsSessionWhenAChunkFails() async throws {
        KeychainStore.set("valid-token", key: "accessToken")

        let fileURL = try makeTempFile(bytes: 10)
        defer { try? FileManager.default.removeItem(at: fileURL) }

        let requests = RequestLog()
        StubURLProtocol.handler = { request in
            let path = request.url?.path ?? ""
            let method = request.httpMethod ?? ""
            requests.record(method: method, path: path)

            if path == "/api/files/uploads" && method == "POST" {
                return .json(201, ["uploadId": "up-2", "chunkSize": 4])
            }
            if path.hasPrefix("/api/files/uploads/up-2/chunks/") && method == "PUT" {
                return StubResponse(statusCode: 500, headers: [:], body: Data("{\"error\":\"boom\"}".utf8))
            }
            if path == "/api/files/uploads/up-2" && method == "DELETE" {
                return StubResponse(statusCode: 204, headers: [:], body: Data())
            }
            return StubResponse(statusCode: 404, headers: [:], body: Data())
        }

        let client = makeClient(singleShotUploadThreshold: 0)

        do {
            _ = try await client.uploadFile(fileURL: fileURL)
            XCTFail("expected the upload to throw once a chunk fails")
        } catch let error as APIError {
            XCTAssertEqual(error.errorDescription, "boom")
        }

        // The failed chunk short-circuits the loop: complete is never
        // reached, but the session is aborted so its scratch file doesn't
        // linger server-side until its idle timeout.
        XCTAssertEqual(requests.entries.filter { $0.method == "DELETE" && $0.path == "/api/files/uploads/up-2" }.count, 1)
        XCTAssertTrue(requests.entries.allSatisfy { !($0.method == "POST" && $0.path.hasSuffix("/complete")) })
    }

    // Pins the fix this was written for: a file at or below the default
    // 8MiB threshold must go through the original single-shot multipart
    // endpoint (one request) rather than the chunked-upload session
    // protocol (initiate + chunk + complete = three round trips), which
    // used to run unconditionally and made folder sync's "re-upload a
    // changed file on every save" noticeably slower for ordinary
    // source-sized files.
    func testUploadFileSmallFileUsesSingleShotEndpointNotChunkedSession() async throws {
        KeychainStore.set("valid-token", key: "accessToken")

        let fileURL = try makeTempFile(bytes: 10)
        defer { try? FileManager.default.removeItem(at: fileURL) }

        let requests = RequestLog()
        StubURLProtocol.handler = { request in
            let path = request.url?.path ?? ""
            let method = request.httpMethod ?? ""
            requests.record(method: method, path: path)

            if path == "/api/files" && method == "POST" {
                return .json(201, [
                    "id": 9, "name": "big.bin", "extension": "bin", "size": 10,
                    "createdAt": "2026-09-12T10:00:00Z", "modifiedAt": "2026-09-12T10:00:00Z",
                    "uploadedBy": "alice", "editedBy": "alice",
                ] as [String: Any])
            }
            return StubResponse(statusCode: 404, headers: [:], body: Data())
        }

        // Default threshold (8MiB) this time -- a 10-byte file must
        // legitimately qualify for the fast path on its own.
        let client = makeClient()
        let progress = ProgressLog()
        let created = try await client.uploadFile(fileURL: fileURL) { sent, total in
            progress.record(sent: sent, total: total)
        }

        XCTAssertEqual(created.id, 9)
        XCTAssertEqual(requests.entries.count, 1)
        XCTAssertEqual(requests.entries[0].method, "POST")
        XCTAssertEqual(requests.entries[0].path, "/api/files")
        XCTAssertTrue(requests.entries.allSatisfy { !$0.path.contains("/uploads") })

        // A single "fully done" progress callback, not a per-chunk trickle.
        XCTAssertEqual(progress.entries.map(\.sent), [10])
        XCTAssertEqual(progress.entries.map(\.total), [10])
    }
}

/// Thread-safe log of (method, path) pairs in call order, for asserting the
/// exact sequence of requests a multi-step flow (like chunked upload) makes.
final class RequestLog: @unchecked Sendable {
    struct Entry { let method: String; let path: String }
    private let lock = NSLock()
    private var _entries: [Entry] = []

    var entries: [Entry] {
        lock.lock()
        defer { lock.unlock() }
        return _entries
    }

    func record(method: String, path: String) {
        lock.lock()
        defer { lock.unlock() }
        _entries.append(Entry(method: method, path: path))
    }
}

/// Thread-safe log of (sent, total) progress callback invocations, in order.
final class ProgressLog: @unchecked Sendable {
    struct Entry { let sent: Int64; let total: Int64 }
    private let lock = NSLock()
    private var _entries: [Entry] = []

    var entries: [Entry] {
        lock.lock()
        defer { lock.unlock() }
        return _entries
    }

    func record(sent: Int64, total: Int64) {
        lock.lock()
        defer { lock.unlock() }
        _entries.append(Entry(sent: sent, total: total))
    }
}
