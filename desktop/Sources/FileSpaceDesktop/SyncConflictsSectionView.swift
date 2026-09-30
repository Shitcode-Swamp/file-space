// Inline conflict list for the existing footer SyncPanelView (REQUIREMENTS.md
// §6.5: when both sides changed, ask, never resolve silently). Deliberately
// not a separate sheet/toolbar pill — see the reverted commit 90b2018 this
// intentionally does *not* repeat; this just adds a section to the sync
// panel that's already there.
import SwiftUI

struct SyncConflictsSectionView: View {
    @ObservedObject var viewModel: SyncViewModel
    @State private var resolvingNames: Set<String> = []

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("\(viewModel.conflicts.count) file\(viewModel.conflicts.count == 1 ? "" : "s") changed in two places")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.orange)

            ForEach(viewModel.conflicts) { conflict in
                VStack(alignment: .leading, spacing: 4) {
                    Text(conflict.name)
                        .font(.caption)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Text("Local: \(byteCount(conflict.localSize)), \(conflict.localModifiedAt.formatted(date: .abbreviated, time: .shortened))  ·  Remote: \(byteCount(conflict.remoteSize)) by \(conflict.remoteEditedBy)")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                    HStack(spacing: 6) {
                        Button("Keep Local") { resolve(conflict) { await viewModel.resolveKeepLocal(conflict) } }
                        Button("Keep Remote") { resolve(conflict) { await viewModel.resolveKeepRemote(conflict) } }
                        Button("Keep Both") { resolve(conflict) { await viewModel.resolveKeepBoth(conflict) } }
                    }
                    .font(.caption)
                    .disabled(resolvingNames.contains(conflict.name))
                }
                .padding(6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .overlay(RoundedRectangle(cornerRadius: 6).stroke(.orange.opacity(0.35)))
            }
        }
    }

    private func resolve(_ conflict: SyncConflict, _ action: @escaping () async -> Void) {
        resolvingNames.insert(conflict.name)
        Task {
            await action()
            resolvingNames.remove(conflict.name)
        }
    }

    private func byteCount(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
    }
}
