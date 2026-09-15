// Conflict resolution (E), reached directly from SyncStatusPillView's
// "attention" state rather than through the sync card — REQUIREMENTS.md
// §6.5: when both sides changed, ask, never resolve silently. Stacks
// through viewModel.conflicts one at a time (resolving one reveals the
// next) rather than making the user find each one individually.
import SwiftUI

struct SyncConflictSheetView: View {
    @ObservedObject var viewModel: SyncViewModel
    @Environment(\.dismiss) private var dismiss
    @State private var isResolving = false

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            if let conflict = viewModel.conflicts.first {
                header(for: conflict)
                versions(for: conflict)
                actions(for: conflict)
            } else {
                VStack(spacing: 10) {
                    Image(systemName: "checkmark.circle.fill")
                        .font(.system(size: 28))
                        .foregroundStyle(.green)
                    Text("All conflicts resolved").font(.headline)
                    Button("Done") { dismiss() }
                        .buttonStyle(.borderedProminent)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 8)
            }
        }
        .padding(20)
        .frame(width: 440)
        .onChange(of: viewModel.conflicts.count) { newCount in
            if newCount == 0 { dismiss() }
        }
    }

    private func header(for conflict: SyncConflict) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                Text("\(conflict.name) changed in two places")
                    .font(.headline)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Text("This file was edited locally and on the server since the last sync. Choose which version to keep.")
                .font(.callout)
                .foregroundStyle(.secondary)
            if viewModel.conflicts.count > 1 {
                Text("\(viewModel.conflicts.count - 1) more file\(viewModel.conflicts.count - 1 == 1 ? "" : "s") waiting after this one.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func versions(for conflict: SyncConflict) -> some View {
        HStack(alignment: .top, spacing: 12) {
            versionCard(title: "Keep local", tint: .indigo, rows: [
                ("Modified", conflict.localModifiedAt.formatted(date: .abbreviated, time: .shortened)),
                ("Size", Self.byteCount(conflict.localSize)),
            ])
            versionCard(title: "Keep remote", tint: .teal, rows: [
                ("Modified", conflict.remoteModifiedAt.formatted(date: .abbreviated, time: .shortened)),
                ("Edited by", conflict.remoteEditedBy),
            ])
        }
    }

    private func versionCard(title: String, tint: Color, rows: [(String, String)]) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title.uppercased())
                .font(.caption2.weight(.bold))
                .foregroundStyle(tint)
            ForEach(rows, id: \.0) { row in
                HStack {
                    Text(row.0).foregroundStyle(.secondary)
                    Spacer()
                    Text(row.1)
                }
                .font(.caption)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(tint.opacity(0.35)))
    }

    private func actions(for conflict: SyncConflict) -> some View {
        HStack(spacing: 8) {
            Button("Keep Local") { resolve { await viewModel.resolveKeepLocal(conflict) } }
                .buttonStyle(.borderedProminent)
            Button("Keep Remote") { resolve { await viewModel.resolveKeepRemote(conflict) } }
                .buttonStyle(.bordered)
            Button("Keep Both") { resolve { await viewModel.resolveKeepBoth(conflict) } }
                .buttonStyle(.bordered)
        }
        .disabled(isResolving)
        .frame(maxWidth: .infinity)
    }

    private func resolve(_ action: @escaping () async -> Void) {
        isResolving = true
        Task {
            await action()
            isResolving = false
        }
    }

    private static func byteCount(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
    }
}
