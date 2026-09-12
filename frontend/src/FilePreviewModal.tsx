import { useEffect } from 'react'
import type { FileRecord } from './types'

/**
 * REQUIREMENTS.md §2 "File Content Display": .java renders as text, .png
 * renders as an image; no other extension gets structured content display.
 * `App.tsx` only ever builds this state for previewable extensions (see
 * `PREVIEWABLE_EXTENSIONS` in columns.tsx), so this component doesn't need
 * to re-check the extension itself.
 */
export type FilePreviewState =
  | { status: 'loading'; file: FileRecord }
  | { status: 'error'; file: FileRecord; message: string }
  | { status: 'text'; file: FileRecord; content: string }
  | { status: 'image'; file: FileRecord; objectUrl: string }

interface FilePreviewModalProps {
  preview: FilePreviewState
  onClose: () => void
}

export function FilePreviewModal({ preview, onClose }: FilePreviewModalProps) {
  // Close on Escape as a nice-to-have (not required per the task).
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return (
    <div
      className="preview-backdrop"
      onClick={onClose}
      role="presentation"
    >
      <div
        className="preview-panel"
        role="dialog"
        aria-modal="true"
        aria-label={`Preview of ${preview.file.name}`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="preview-header">
          <strong>{preview.file.name}</strong>
          <button type="button" onClick={onClose} aria-label="Close preview">
            Close
          </button>
        </div>
        <div className="preview-body">
          {preview.status === 'loading' && <p>Loading preview…</p>}
          {preview.status === 'error' && (
            <p className="error">Failed to load preview: {preview.message}</p>
          )}
          {preview.status === 'text' && <pre className="preview-source">{preview.content}</pre>}
          {preview.status === 'image' && (
            <img className="preview-image" src={preview.objectUrl} alt={preview.file.name} />
          )}
        </div>
      </div>
    </div>
  )
}
