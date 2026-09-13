import SwiftUI

struct ContentView: View {
    @EnvironmentObject private var appState: AppState

    var body: some View {
        if appState.isAuthenticated {
            MainView(appState: appState)
        } else {
            LoginView()
        }
    }
}
