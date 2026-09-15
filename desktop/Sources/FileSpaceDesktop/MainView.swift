import AppKit
import SwiftUI
import UniformTypeIdentifiers

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
    @State private var isDropTargeted = false

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
            if !filesVM.uploadProgress.isEmpty {
                uploadProgressView
            }
            if let error = filesVM.errorMessage {
                Text(error).foregroundStyle(.red).font(.callout).padding(.horizontal)
            }
            table
            Divider()
            SyncPanelView(viewModel: syncVM)
        }
        .task {
            await filesVM.refresh()
            filesVM.startAutoRefresh()
        }
        .onDrop(of: [.fileURL], isTargeted: $isDropTargeted) { providers in
            handleDrop(providers)
        }
        .overlay {
            if isDropTargeted {
                dropOverlay
            }
        }
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
                filesVM.stopAutoRefresh()
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

    private var uploadProgressView: some View {
        VStack(alignment: .leading, spacing: 4) {
            ForEach(filesVM.uploadProgress) { progress in
                HStack {
                    Text(progress.name)
                        .font(.callout)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(width: 200, alignment: .leading)
                    ProgressView(value: progress.fraction)
                    Text("\(Int(progress.fraction * 100))%")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .frame(width: 40, alignment: .trailing)
                }
            }
        }
        .padding(.horizontal)
        .padding(.bottom, 8)
    }

    // A hand-rolled column layout rather than SwiftUI's Table type: Table's
    // column-builder generics don't infer cleanly through conditional
    // (if visibleColumns.contains(...)) columns, whereas a plain List row
    // built with ViewBuilder handles the same conditionals without issue.
    private let nameWidth: CGFloat = 220
    private let columnWidth: CGFloat = 140
    private let actionsWidth: CGFloat = 160

    private func sortHeaderButton(_ column: FileSortColumn, width: CGFloat) -> some View {
        let isActive = filesVM.sortColumn == column
        let symbol = isActive ? filesVM.sortDirection.symbol : SortDirection.none.symbol
        return Button {
            filesVM.toggleSort(column)
        } label: {
            Text("\(column.title) \(symbol)").bold()
        }
        .buttonStyle(.plain)
        .frame(width: width, alignment: .leading)
    }

    private var tableHeader: some View {
        HStack(spacing: 12) {
            sortHeaderButton(.name, width: nameWidth)
            if visibleColumns.contains(.createdAt) {
                sortHeaderButton(.createdAt, width: columnWidth)
            }
            if visibleColumns.contains(.modifiedAt) {
                sortHeaderButton(.modifiedAt, width: columnWidth)
            }
            if visibleColumns.contains(.uploadedBy) {
                sortHeaderButton(.uploadedBy, width: columnWidth)
            }
            if visibleColumns.contains(.editedBy) {
                sortHeaderButton(.editedBy, width: columnWidth)
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
                Text(fileTimestampFormatter.string(from: file.createdAt))
                    .frame(width: columnWidth, alignment: .leading)
            }
            if visibleColumns.contains(.modifiedAt) {
                Text(fileTimestampFormatter.string(from: file.modifiedAt))
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
        panel.allowsMultipleSelection = true
        guard panel.runModal() == .OK, !panel.urls.isEmpty else { return }
        let urls = panel.urls
        Task { await filesVM.upload(fileURLs: urls) }
    }

    private var dropOverlay: some View {
        ZStack {
            Color.accentColor.opacity(0.12)
            RoundedRectangle(cornerRadius: 8)
                .strokeBorder(Color.accentColor, style: StrokeStyle(lineWidth: 3, dash: [8]))
                .padding(8)
            Text("Drop files to upload")
                .font(.title2.bold())
                .padding(12)
                .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 8))
        }
        .allowsHitTesting(false)
    }

    /// Handles files dragged in from Finder, mirroring presentUploadPanel's
    /// own upload(fileURLs:) call. NSItemProvider's load callback isn't
    /// itself MainActor-isolated, so this bridges into structured
    /// concurrency (withCheckedContinuation) rather than calling back into
    /// filesVM directly from it.
    private func handleDrop(_ providers: [NSItemProvider]) -> Bool {
        guard !providers.isEmpty else { return false }
        Task {
            let urls = await Self.loadFileURLs(from: providers)
            guard !urls.isEmpty else { return }
            await filesVM.upload(fileURLs: urls)
        }
        return true
    }

    /// Loads sequentially rather than concurrently (e.g. via TaskGroup):
    /// NSItemProvider isn't Sendable, and dragging in enough files at once
    /// for that to matter performance-wise essentially never happens.
    private static func loadFileURLs(from providers: [NSItemProvider]) async -> [URL] {
        var results: [URL] = []
        for provider in providers {
            let url = await withCheckedContinuation { (continuation: CheckedContinuation<URL?, Never>) in
                _ = provider.loadObject(ofClass: URL.self) { url, _ in
                    continuation.resume(returning: url)
                }
            }
            if let url {
                results.append(url)
            }
        }
        return results
    }
}
