// The toolbar sync status pill (A) — the single, always-visible entry point
// for folder sync (REQUIREMENTS.md §6), replacing the old footer panel
// pinned under the file table. Where a tap lands depends on
// SyncViewModel.pillState: everything routes to the sync card (SyncCardView,
// which itself covers the empty-state prompt when no folder is chosen yet)
// except "attention", which opens the conflict-resolution sheet directly —
// that's the one state where a decision is actually blocking, so showing
// the card first would just be a detour.
import SwiftUI

struct SyncStatusPillView: View {
    @ObservedObject var viewModel: SyncViewModel
    @State private var showCard = false
    @State private var showConflicts = false

    var body: some View {
        Button(action: handleTap) {
            HStack(spacing: 6) {
                Circle().fill(dotColor).frame(width: 7, height: 7)
                Text(label).font(.caption.weight(.semibold))
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 4)
            .background(dotColor.opacity(0.15), in: Capsule())
            .foregroundStyle(dotColor)
        }
        .buttonStyle(.plain)
        .popover(isPresented: $showCard) {
            SyncCardView(viewModel: viewModel, showConflicts: $showConflicts)
        }
        .sheet(isPresented: $showConflicts) {
            SyncConflictSheetView(viewModel: viewModel)
        }
    }

    private func handleTap() {
        switch viewModel.pillState {
        case .attention:
            showConflicts = true
        default:
            showCard = true
        }
    }

    private var label: String {
        switch viewModel.pillState {
        case .notSetUp: return "Set up sync"
        case .paused: return "Paused"
        case .watching: return "Watching"
        case .syncing(let count): return count > 1 ? "Syncing \(count)" : "Syncing"
        case .attention(let count): return count == 1 ? "1 needs you" : "\(count) need you"
        case .error: return "Sync error"
        }
    }

    private var dotColor: Color {
        switch viewModel.pillState {
        case .notSetUp, .paused: return .secondary
        case .watching: return .green
        case .syncing: return .teal
        case .attention: return .orange
        case .error: return .red
        }
    }
}
