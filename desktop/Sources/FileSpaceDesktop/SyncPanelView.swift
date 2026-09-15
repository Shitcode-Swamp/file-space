// The sync card (REQUIREMENTS.md §6's UX): opened from SyncStatusPillView in
// the toolbar rather than pinned as permanent real estate under the file
// table. Shows the empty state (no folder chosen yet) or the normal
// overview/activity pages depending on SyncViewModel's state.
import SwiftUI

struct SyncCardView: View {
    @ObservedObject var viewModel: SyncViewModel
    @Binding var showConflicts: Bool

    private enum Page { case overview, activity }
    @State private var page: Page = .overview

    var body: some View {
        Group {
            if viewModel.folderPath == nil {
                emptyState
            } else if page == .activity {
                activityPage
            } else {
                overviewPage
            }
        }
        .padding(16)
        .frame(width: 340)
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "arrow.triangle.2.circlepath")
                .font(.system(size: 28))
                .foregroundStyle(.secondary)
            Text("No folder is syncing yet")
                .font(.headline)
            Text("Pick a folder and FileSpace keeps it in sync automatically — new files upload, remote changes download, deletions match on both sides.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            Button("Choose a folder…") { chooseAndStart() }
                .buttonStyle(.borderedProminent)
                .padding(.top, 4)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 8)
    }

    private var overviewPage: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("SYNCED FOLDER")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)
                    Text((viewModel.folderPath as NSString?)?.lastPathComponent ?? "")
                        .font(.callout.weight(.semibold))
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                Spacer()
                runningBadge
            }

            Text(viewModel.statusMessage)
                .font(.caption)
                .foregroundStyle(.secondary)

            if let lastError = viewModel.lastError {
                Text(lastError)
                    .font(.caption)
                    .foregroundStyle(.red)
            }

            if !viewModel.conflicts.isEmpty {
                Button {
                    showConflicts = true
                } label: {
                    Label("\(viewModel.conflicts.count) file\(viewModel.conflicts.count == 1 ? "" : "s") need\(viewModel.conflicts.count == 1 ? "s" : "") a decision", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption.weight(.semibold))
                }
                .buttonStyle(.borderless)
                .foregroundStyle(.orange)
            }

            Divider()

            HStack {
                Button(viewModel.isRunning ? "Pause" : "Resume") {
                    if viewModel.isRunning { viewModel.stop() } else { viewModel.start() }
                }
                Button("View activity") { page = .activity }
                Spacer()
                Button("Change folder…") { chooseAndStart() }
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
        }
    }

    private var activityPage: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Button {
                    page = .overview
                } label: {
                    Label("Back", systemImage: "chevron.left")
                }
                .buttonStyle(.borderless)
                Spacer()
                Text("Activity").font(.callout.weight(.semibold))
                Spacer()
                // Balances the "Back" button so the title stays centered.
                Label("Back", systemImage: "chevron.left").opacity(0).accessibilityHidden(true)
            }
            Divider()
            SyncActivityListView(entries: viewModel.log)
        }
    }

    private var runningBadge: some View {
        Text(viewModel.isRunning ? "Watching" : "Paused")
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(viewModel.isRunning ? Color.green.opacity(0.15) : Color.secondary.opacity(0.15), in: Capsule())
            .foregroundStyle(viewModel.isRunning ? .green : .secondary)
    }

    private func chooseAndStart() {
        viewModel.chooseFolder()
        if viewModel.folderPath != nil {
            viewModel.start()
        }
    }
}
