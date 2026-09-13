import AppKit
import SwiftUI

enum ColumnKind: CaseIterable {
    case createdAt, modifiedAt, uploadedBy, editedBy

    var title: String {
        switch self {
        case .createdAt: return "Created"
        case .modifiedAt: return "Modified"
        case .uploadedBy: return "Uploaded by"
        case .editedBy: return "Edited by"
        }
    }
}

/// The signed-in drive view: file table (REQUIREMENTS.md §3), sort/filter
/// (§4), upload/download/delete, and the folder-sync panel below it.
struct MainView: View {
    @ObservedObject var appState: AppState
    @StateObject private var filesVM: FilesViewModel
    @StateObject private var syncVM: SyncViewModel

    // "name" always stays visible (REQUIREMENTS.md §3); the rest can be
    // toggled, mirroring the web client's column-visibility checkboxes.
    @State private var visibleColumns: Set<ColumnKind> = Set(ColumnKind.allCases)
    @State private var fileToDelete: FileRecord?
    @State private var previewFile: FileRecord?

    init(appState: AppState) {
        self.appState = appState
        let api = appState.api
        _filesVM = StateObject(wrappedValue: FilesViewModel(api: api, onUnauthorized: { appState.logout() }))
        _syncVM = StateObject(wrappedValue: SyncViewModel(api: api, onUnauthorized: { appState.logout() }))
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            toolbar
            if let error = filesVM.errorMessage {
                Text(error).foregroundStyle(.red).font(.callout).padding(.horizontal)
            }
            table
            Divider()
            SyncPanelView(viewModel: syncVM)
        }
        .task { await filesVM.refresh() }
        .alert(
            "Delete file?",
            isPresented: Binding(get: { fileToDelete != nil }, set: { if !$0 { fileToDelete = nil } }),
            presenting: fileToDelete
        ) { file in
            Button("Delete", role: .destructive) {
                Task { await filesVM.delete(file) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { file in
            Text("This will permanently delete \(file.name).")
        }
        .sheet(item: $previewFile) { file in
            FilePreviewView(file: file, viewModel: filesVM)
        }
    }

    private var header: some View {
        HStack {
            Text("FileSpace").font(.title2.bold())
            Spacer()
            Text("Logged in as \(appState.username ?? "")").foregroundStyle(.secondary)
            Button("Log out") {
                syncVM.stop()
                appState.logout()
            }
        }
        .padding()
    }

    private var toolbar: some View {
        HStack {
            Button("Refresh") { Task { await filesVM.refresh() } }
            Button("Upload…") { presentUploadPanel() }

            Picker("Filter", selection: $filesVM.extensionFilter) {
                Text("All files").tag(String?.none)
                ForEach(filesVM.availableExtensions, id: \.self) { ext in
                    Text(".\(ext)").tag(String?.some(ext))
                }
            }
            .frame(width: 160)
            .labelsHidden()

            Button {
                filesVM.editedBySortOrder.cycle()
            } label: {
                Text("Edited by \(filesVM.editedBySortOrder.symbol)")
            }

            Menu("Columns") {
                ForEach(ColumnKind.allCases, id: \.self) { column in
                    Toggle(column.title, isOn: Binding(
                        get: { visibleColumns.contains(column) },
                        set: { isOn in
                            if isOn { visibleColumns.insert(column) } else { visibleColumns.remove(column) }
                        }
                    ))
                }
            }

            Spacer()
            if filesVM.isLoading { ProgressView().scaleEffect(0.6) }
        }
        .padding(.horizontal)
        .padding(.vertical, 8)
    }

    // A hand-rolled column layout rather than SwiftUI's Table type: Table's
    // column-builder generics don't infer cleanly through conditional
    // (if visibleColumns.contains(...)) columns, whereas a plain List row
    // built with ViewBuilder handles the same conditionals without issue.
    private let nameWidth: CGFloat = 220
    private let columnWidth: CGFloat = 140
    private let actionsWidth: CGFloat = 160

    private var tableHeader: some View {
        HStack(spacing: 12) {
            Text("Name").bold().frame(width: nameWidth, alignment: .leading)
            if visibleColumns.contains(.createdAt) {
                Text("Created").bold().frame(width: columnWidth, alignment: .leading)
            }
            if visibleColumns.contains(.modifiedAt) {
                Text("Modified").bold().frame(width: columnWidth, alignment: .leading)
            }
            if visibleColumns.contains(.uploadedBy) {
                Text("Uploaded by").bold().frame(width: columnWidth, alignment: .leading)
            }
            if visibleColumns.contains(.editedBy) {
                Text("Edited by").bold().frame(width: columnWidth, alignment: .leading)
            }
            Text("Actions").bold().frame(width: actionsWidth, alignment: .leading)
            Spacer(minLength: 0)
        }
        .padding(.horizontal)
        .padding(.vertical, 4)
    }

    private func row(for file: FileRecord) -> some View {
        HStack(spacing: 12) {
            Group {
                if PreviewSupport.isPreviewable(file.extensionName) {
                    Button(file.name) { previewFile = file }.buttonStyle(.link)
                } else {
                    Text(file.name)
                }
            }
            .frame(width: nameWidth, alignment: .leading)

            if visibleColumns.contains(.createdAt) {
                Text(file.createdAt.formatted(date: .abbreviated, time: .shortened))
                    .frame(width: columnWidth, alignment: .leading)
            }
            if visibleColumns.contains(.modifiedAt) {
                Text(file.modifiedAt.formatted(date: .abbreviated, time: .shortened))
                    .frame(width: columnWidth, alignment: .leading)
            }
            if visibleColumns.contains(.uploadedBy) {
                Text(file.uploadedBy).frame(width: columnWidth, alignment: .leading)
            }
            if visibleColumns.contains(.editedBy) {
                Text(file.editedBy).frame(width: columnWidth, alignment: .leading)
            }

            HStack {
                Button("Download") { Task { await filesVM.download(file) } }
                Button("Delete") { fileToDelete = file }
            }
            .frame(width: actionsWidth, alignment: .leading)

            Spacer(minLength: 0)
        }
    }

    private var table: some View {
        VStack(spacing: 0) {
            tableHeader
            Divider()
            if filesVM.displayedFiles.isEmpty {
                Text("No files yet.").foregroundStyle(.secondary).padding()
                Spacer()
            } else {
                List(filesVM.displayedFiles) { file in
                    row(for: file)
                }
            }
        }
    }

    private func presentUploadPanel() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        guard panel.runModal() == .OK, let url = panel.url else { return }
        Task { await filesVM.upload(fileURL: url) }
    }
}
