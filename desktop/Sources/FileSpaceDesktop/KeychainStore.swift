// Token storage via the macOS Keychain (REQUIREMENTS.md §5.4: "Token
// storage: macOS Keychain (Security framework), not UserDefaults"). Plain
// generic-password items, one per key, under a single service name.

import Foundation
import Security

enum KeychainStore {
    private static let service = "com.filespace.desktop"

    private static func query(for key: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]
    }

    static func set(_ value: String, key: String) {
        SecItemDelete(query(for: key) as CFDictionary)
        var attributes = query(for: key)
        attributes[kSecValueData as String] = Data(value.utf8)
        SecItemAdd(attributes as CFDictionary, nil)
    }

    static func read(_ key: String) -> String? {
        var attributes = query(for: key)
        attributes[kSecReturnData as String] = true
        attributes[kSecMatchLimit as String] = kSecMatchLimitOne

        var result: AnyObject?
        let status = SecItemCopyMatching(attributes as CFDictionary, &result)
        guard status == errSecSuccess, let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    static func delete(_ key: String) {
        SecItemDelete(query(for: key) as CFDictionary)
    }

    // Convenience accessors for the three values the app actually stores.
    static var accessToken: String? { read("accessToken") }
    static var refreshToken: String? { read("refreshToken") }
    static var username: String? { read("username") }

    static func clearSession() {
        delete("accessToken")
        delete("refreshToken")
        delete("username")
    }
}
