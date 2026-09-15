import { useEffect } from 'react'
import type { Column } from '@tanstack/react-table'
import type { FileRecord } from './types'

interface SettingsModalProps {
  columns: Column<FileRecord, unknown>[]
  onClose: () => void
}

function columnLabel(column: Column<FileRecord, unknown>): string {
  const header = column.columnDef.header
  return typeof header === 'string' ? header : column.id
}

export function SettingsModal({ columns, onClose }: SettingsModalProps) {
  // Close on Escape, matching FilePreviewModal.
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return (
    <div className="modal-backdrop" onClick={onClose} role="presentation">
      <div
        className="modal-panel"
        role="dialog"
        aria-modal="true"
        aria-label="Settings"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="modal-header">
          <strong>Settings</strong>
          <button type="button" onClick={onClose} aria-label="Close settings">
            Close
          </button>
        </div>
        <div className="modal-body">
          <fieldset className="settings-column-list">
            <legend>Columns</legend>
            {columns
              .filter((column) => column.getCanHide())
              .map((column) => (
                <label key={column.id}>
                  <input
                    type="checkbox"
                    checked={column.getIsVisible()}
                    onChange={column.getToggleVisibilityHandler()}
                  />
                  {columnLabel(column)}
                </label>
              ))}
          </fieldset>
        </div>
      </div>
    </div>
  )
}
