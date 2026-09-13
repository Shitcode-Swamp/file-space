// Local-change trigger for sync (REQUIREMENTS.md §6.4). Uses
// DispatchSource file-system-object monitoring on the folder itself — the
// alternative REQUIREMENTS.md explicitly sanctions ("FSEvents (or
// DispatchSource file monitoring)") — rather than the lower-level FSEvents
// C API, since a directory-level watch is sufficient here: this app's sync
// folder is flat (no subfolders), and in-place content edits that don't
// change directory entries are still caught by SyncViewModel's periodic
// poll, which rescans file contents/mtimes directly.

import Dispatch
import Foundation

final class DirectoryWatcher {
    private let path: String
    private let onChange: () -> Void
    private var source: DispatchSourceFileSystemObject?
    private var descriptor: CInt = -1

    init(path: String, onChange: @escaping () -> Void) {
        self.path = path
        self.onChange = onChange
    }

    func start() {
        stop()
        descriptor = open(path, O_EVTONLY)
        guard descriptor >= 0 else { return }

        let source = DispatchSource.makeFileSystemObjectSource(
            fileDescriptor: descriptor,
            eventMask: [.write, .delete, .rename],
            queue: .main
        )
        source.setEventHandler { [onChange] in onChange() }
        source.setCancelHandler { [descriptor] in close(descriptor) }
        source.resume()
        self.source = source
    }

    func stop() {
        source?.cancel()
        source = nil
        descriptor = -1
    }

    deinit {
        source?.cancel()
    }
}
