// Round-trip coverage for KeychainStore (desktop/Sources/FileSpaceDesktop/KeychainStore.swift).
// Uses distinct test-only keys (never "accessToken" / "refreshToken" /
// "username") so these tests can't collide with a real running instance of
// the app's own keychain items, and deletes whatever it sets in tearDown so
// repeated test runs don't accumulate stale keychain entries.

import XCTest
@testable import FileSpaceDesktop

final class KeychainStoreTests: XCTestCase {
    private let testKeyA = "com.filespace.desktopTests.keyA"
    private let testKeyB = "com.filespace.desktopTests.keyB"

    override func tearDown() {
        KeychainStore.delete(testKeyA)
        KeychainStore.delete(testKeyB)
        super.tearDown()
    }

    func testSetThenReadReturnsStoredValue() {
        KeychainStore.set("hello-world", key: testKeyA)
        XCTAssertEqual(KeychainStore.read(testKeyA), "hello-world")
    }

    func testSetOverwritesPreviousValueForSameKey() {
        KeychainStore.set("first", key: testKeyA)
        KeychainStore.set("second", key: testKeyA)
        XCTAssertEqual(KeychainStore.read(testKeyA), "second")
    }

    func testDeleteRemovesStoredValue() {
        KeychainStore.set("to-be-deleted", key: testKeyA)
        KeychainStore.delete(testKeyA)
        XCTAssertNil(KeychainStore.read(testKeyA))
    }

    func testReadOfNeverSetKeyIsNil() {
        XCTAssertNil(KeychainStore.read(testKeyB))
    }

    func testDistinctKeysDoNotCollide() {
        KeychainStore.set("value-a", key: testKeyA)
        KeychainStore.set("value-b", key: testKeyB)
        XCTAssertEqual(KeychainStore.read(testKeyA), "value-a")
        XCTAssertEqual(KeychainStore.read(testKeyB), "value-b")
    }
}
