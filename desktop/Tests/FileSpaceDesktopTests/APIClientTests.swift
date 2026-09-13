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

    private func makeClient() -> APIClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        let session = URLSession(configuration: config)
        return APIClient(baseURL: URL(string: "http://stub.invalid")!, session: session)
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
}
