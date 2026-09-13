// FileSpaceDesktop — SwiftUI macOS client for file-space (REQUIREMENTS.md
// §5.4). Auth + file browsing/upload/download/delete/preview mirror the web
// client; folder sync is this client's own responsibility (see
// SyncViewModel.swift for the protocol's scope boundary).

import AppKit
import SwiftUI

/// `swift run` launches this as a plain process, not a signed .app bundle,
/// so nothing tells the window server this app should become the frontmost,
/// key window on launch. Without this, the window appears and accepts
/// clicks but never gets keyboard focus — text fields look unresponsive.
final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        NSApp.windows.first?.makeKeyAndOrderFront(nil)
    }
}

@main
struct FileSpaceDesktopApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(appState)
                .frame(minWidth: 760, minHeight: 560)
        }
    }
}
