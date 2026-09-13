// App-wide session state: who's logged in, backed by KeychainStore so a
// relaunch resumes the session without asking to log in again.

import Foundation

@MainActor
final class AppState: ObservableObject {
    @Published private(set) var username: String?
    @Published var isBusy = false
    @Published var errorMessage: String?

    let api = APIClient()

    init() {
        username = KeychainStore.username
    }

    var isAuthenticated: Bool { username != nil }

    func register(username: String, password: String) async {
        errorMessage = nil
        isBusy = true
        defer { isBusy = false }
        do {
            _ = try await api.register(username: username, password: password)
            try await performLogin(username: username, password: password)
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func login(username: String, password: String) async {
        errorMessage = nil
        isBusy = true
        defer { isBusy = false }
        do {
            try await performLogin(username: username, password: password)
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func performLogin(username: String, password: String) async throws {
        let response = try await api.login(username: username, password: password)
        KeychainStore.set(response.accessToken, key: "accessToken")
        KeychainStore.set(response.refreshToken, key: "refreshToken")
        KeychainStore.set(username, key: "username")
        self.username = username
    }

    func logout() {
        KeychainStore.clearSession()
        username = nil
    }
}
