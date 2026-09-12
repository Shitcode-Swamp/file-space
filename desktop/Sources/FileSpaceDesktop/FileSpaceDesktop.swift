// FileSpaceDesktop
// Minimal SwiftUI app entry point. Networking, auth, and FSEvents-based
// sync (REQUIREMENTS.md §5.4 / §6) are implemented in later phases.

import SwiftUI

@main
struct FileSpaceDesktopApp: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
}
