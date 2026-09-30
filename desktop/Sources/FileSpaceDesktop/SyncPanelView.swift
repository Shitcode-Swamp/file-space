import SwiftUI

struct SyncPanelView: View {
    @ObservedObject var viewModel: SyncViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Folder sync").font(.headline)
                Spacer()
                if viewModel.isSyncing {
                    ProgressView().scaleEffect(0.6).frame(width: 14, height: 14)
                    Text("Syncing…").foregroundStyle(.secondary).font(.caption)
                } else {
                    Text(viewModel.statusMessage).foregroundStyle(.secondary).font(.caption)
                }
            }
            HStack {
                Text(viewModel.folderPath ?? "No folder selected")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer()
                Button("Choose Folder…") { viewModel.chooseFolder() }
                if viewModel.isRunning {
                    Button("Stop Sync") { viewModel.stop() }
                } else {
                    Button("Start Sync") { viewModel.start() }
                        .disabled(viewModel.folderPath == nil)
                }
            }
            if !viewModel.conflicts.isEmpty {
                SyncConflictsSectionView(viewModel: viewModel)
            }
            if !viewModel.log.isEmpty {
                ScrollView {
                    VStack(alignment: .leading, spacing: 2) {
                        ForEach(viewModel.log.suffix(10).reversed()) { line in
                            Text(line.text).font(.caption2).foregroundStyle(.secondary)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 80)
            }
        }
        .padding()
    }
}
