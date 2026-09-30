import { useEffect } from 'react'
import { useSync } from './SyncContext'
import { SyncConflictsSection } from './SyncConflictsSection'

interface SyncModalProps {
  onClose: () => void
}

/** Folder sync UI (REQUIREMENTS.md §5.3/§6.4): a manual "Sync now" button
 * driving the same one-shot File System Access API + manifest-diff flow
 * desktop's SyncPanelView/SyncViewModel run continuously in the
 * background. Reuses the existing .modal-* chrome (see SettingsModal.tsx),
 * including Escape-to-close. */
export function SyncModal({ onClose }: SyncModalProps) {
  const {
    isSupported,
    folderName,
    isSyncing,
    conflicts,
    log,
    permissionNeeded,
    lastError,
    chooseFolder,
    syncNow,
    resolveKeepLocal,
    resolveKeepRemote,
    resolveKeepBoth,
  } = useSync()

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
        aria-label="Folder sync"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="modal-header">
          <strong>Folder sync</strong>
          <button type="button" onClick={onClose} aria-label="Close folder sync">
            Close
          </button>
        </div>
        <div className="modal-body">
          {!isSupported ? (
            <p className="sync-unsupported">
              Folder sync needs a Chromium-based browser (Chrome, Edge, Brave) — it isn't supported in
              this browser.
            </p>
          ) : (
            <>
              <div className="sync-folder-row">
                <span className="sync-folder-path">{folderName ?? 'No folder selected'}</span>
                <div className="sync-folder-actions">
                  <button type="button" onClick={() => void chooseFolder()}>
                    Choose Folder…
                  </button>
                  <button type="button" onClick={() => void syncNow()} disabled={!folderName || isSyncing}>
                    {isSyncing ? 'Syncing…' : 'Sync now'}
                  </button>
                </div>
              </div>

              {permissionNeeded && (
                <p className="sync-permission-notice">
                  Permission needed for this folder — click "Sync now" to grant it.
                </p>
              )}
              {lastError && <p className="error">{lastError}</p>}

              <SyncConflictsSection
                conflicts={conflicts}
                onKeepLocal={resolveKeepLocal}
                onKeepRemote={resolveKeepRemote}
                onKeepBoth={resolveKeepBoth}
              />

              {log.length > 0 && (
                <ul className="sync-log">
                  {log
                    .slice(-10)
                    .reverse()
                    .map((line, index) => (
                      <li key={index}>{line}</li>
                    ))}
                </ul>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}
