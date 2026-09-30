// Ambient declarations for the two pieces of the File System Access API
// that TypeScript's bundled lib.dom.d.ts doesn't cover yet:
// `window.showDirectoryPicker()` and the permission-query/-request methods
// on a handle. Everything else this app uses (FileSystemDirectoryHandle's
// `.values()`/`.getFileHandle()`/`.removeEntry()`, FileSystemFileHandle's
// `.getFile()`/`.createWritable()`) is already declared by lib.dom.d.ts.
//
// This API is Chromium-only (Chrome/Edge/Brave) — see
// sync/SyncModal.tsx's `'showDirectoryPicker' in window` runtime guard,
// which is the actual browser-support check; these are just types.

interface FileSystemPermissionDescriptor {
  mode?: 'read' | 'readwrite'
}

interface FileSystemHandle {
  queryPermission(descriptor?: FileSystemPermissionDescriptor): Promise<PermissionState>
  requestPermission(descriptor?: FileSystemPermissionDescriptor): Promise<PermissionState>
}

interface DirectoryPickerOptions {
  mode?: 'read' | 'readwrite'
}

interface Window {
  showDirectoryPicker(options?: DirectoryPickerOptions): Promise<FileSystemDirectoryHandle>
}
