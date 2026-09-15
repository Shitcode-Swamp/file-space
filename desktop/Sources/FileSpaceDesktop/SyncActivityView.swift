// The filterable activity history (D) shown inside SyncCardView's activity
// page — a structured replacement for what used to be a flat, undifferentiated
// scroll of gray caption text (SyncViewModel.log is now [SyncLogEntry], not
// [String]).
import SwiftUI

struct SyncActivityListView: View {
    let entries: [SyncLogEntry]

    private enum Filter: String, CaseIterable {
        case all = "All"
        case uploads = "Uploads"
        case downloads = "Downloads"
        case attention = "Needs attention"
    }
    @State private var filter: Filter = .all

    private var filtered: [SyncLogEntry] {
        let newestFirst = entries.reversed()
        switch filter {
        case .all: return Array(newestFirst)
        case .uploads: return newestFirst.filter { $0.kind == .upload }
        case .downloads: return newestFirst.filter { $0.kind == .download }
        case .attention: return newestFirst.filter { $0.kind == .conflict || $0.kind == .error }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                ForEach(Filter.allCases, id: \.self) { candidate in
                    Button(candidate.rawValue) { filter = candidate }
                        .buttonStyle(.bordered)
                        .controlSize(.mini)
                        .tint(filter == candidate ? .accentColor : .secondary)
                }
            }

            if filtered.isEmpty {
                Text(entries.isEmpty ? "No activity yet." : "Nothing here.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 16)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(filtered) { entry in
                            SyncActivityRow(entry: entry)
                            if entry.id != filtered.last?.id {
                                Divider()
                            }
                        }
                    }
                }
                .frame(maxHeight: 220)
            }
        }
    }
}

private struct SyncActivityRow: View {
    let entry: SyncLogEntry

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: iconName)
                .foregroundStyle(iconColor)
                .frame(width: 16)
            Text(entry.message)
                .font(.caption)
                .frame(maxWidth: .infinity, alignment: .leading)
            Text(entry.timestamp.formatted(date: .omitted, time: .shortened))
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 5)
    }

    private var iconName: String {
        switch entry.kind {
        case .upload: return "arrow.up.circle.fill"
        case .download: return "arrow.down.circle.fill"
        case .deleteLocal, .deleteRemote: return "trash.circle.fill"
        case .conflict: return "exclamationmark.triangle.fill"
        case .error: return "xmark.circle.fill"
        }
    }

    private var iconColor: Color {
        switch entry.kind {
        case .upload: return .indigo
        case .download: return .teal
        case .deleteLocal, .deleteRemote: return .secondary
        case .conflict: return .orange
        case .error: return .red
        }
    }
}
