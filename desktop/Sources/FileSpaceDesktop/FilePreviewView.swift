import AppKit
import SwiftUI

struct FilePreviewView: View {
    let file: FileRecord
    @ObservedObject var viewModel: FilesViewModel
    @Environment(\.dismiss) private var dismiss

    @State private var content: PreviewContent?
    @State private var isLoading = true

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text(file.name).font(.headline)
                Spacer()
                Button("Close") { dismiss() }
            }
            .padding()

            Divider()

            Group {
                if isLoading {
                    ProgressView()
                } else if let content {
                    switch content {
                    case .text(let text):
                        ScrollView {
                            Text(text)
                                .font(.system(.body, design: .monospaced))
                                .textSelection(.enabled)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .padding()
                        }
                    case .image(let data):
                        if let nsImage = NSImage(data: data) {
                            Image(nsImage: nsImage)
                                .resizable()
                                .scaledToFit()
                                .padding()
                        } else {
                            Text("Unable to decode image").foregroundStyle(.secondary)
                        }
                    }
                } else if let error = viewModel.errorMessage {
                    Text(error).foregroundStyle(.red).padding()
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .frame(minWidth: 520, minHeight: 420)
        .task {
            content = await viewModel.fetchPreview(file)
            isLoading = false
        }
    }
}
