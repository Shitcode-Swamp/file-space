import { useState } from 'react'
import type { SyncConflict } from './reconcile'

interface SyncConflictsSectionProps {
  conflicts: SyncConflict[]
  onKeepLocal: (conflict: SyncConflict) => Promise<void>
  onKeepRemote: (conflict: SyncConflict) => Promise<void>
  onKeepBoth: (conflict: SyncConflict) => Promise<void>
}

function byteCount(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let value = bytes / 1024
  let unitIndex = 0
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024
    unitIndex += 1
  }
  return `${value.toFixed(1)} ${units[unitIndex]}`
}

/** Inline conflict list for SyncModal (REQUIREMENTS.md §6.5: when both
 * sides changed, ask, never resolve silently). Mirrors desktop's
 * SyncConflictsSectionView.swift. */
export function SyncConflictsSection({
  conflicts,
  onKeepLocal,
  onKeepRemote,
  onKeepBoth,
}: SyncConflictsSectionProps) {
  const [resolvingNames, setResolvingNames] = useState<Set<string>>(new Set())

  if (conflicts.length === 0) return null

  const resolve = (conflict: SyncConflict, action: (c: SyncConflict) => Promise<void>) => {
    setResolvingNames((prev) => new Set(prev).add(conflict.name))
    void action(conflict).finally(() => {
      setResolvingNames((prev) => {
        const next = new Set(prev)
        next.delete(conflict.name)
        return next
      })
    })
  }

  return (
    <div className="sync-conflicts">
      <p className="sync-conflicts-heading">
        {conflicts.length} file{conflicts.length === 1 ? '' : 's'} changed in two places
      </p>
      {conflicts.map((conflict) => {
        const isResolving = resolvingNames.has(conflict.name)
        return (
          <div key={conflict.name} className="sync-conflict-item">
            <p className="sync-conflict-name">{conflict.name}</p>
            <p className="sync-conflict-details">
              Local: {byteCount(conflict.localSize)}, {new Date(conflict.localMtime).toLocaleString()} ·
              Remote: {byteCount(conflict.remoteSize)} by {conflict.remoteEditedBy}
            </p>
            <div className="sync-conflict-actions">
              <button type="button" disabled={isResolving} onClick={() => resolve(conflict, onKeepLocal)}>
                Keep Local
              </button>
              <button type="button" disabled={isResolving} onClick={() => resolve(conflict, onKeepRemote)}>
                Keep Remote
              </button>
              <button type="button" disabled={isResolving} onClick={() => resolve(conflict, onKeepBoth)}>
                Keep Both
              </button>
            </div>
          </div>
        )
      })}
    </div>
  )
}
