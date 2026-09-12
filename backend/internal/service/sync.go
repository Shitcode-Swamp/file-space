package service

import (
	"context"
	"fmt"

	"filespace/backend/internal/repo"
)

// SyncAction describes one action a client should take to bring its local
// copy of a user's folder back in line with the server, per
// REQUIREMENTS.md §6.6's response shape.
type SyncAction struct {
	// Name is the file's name.
	Name string
	// Action is "download" (the file is new or was modified server-side
	// and the client should fetch its current content) or "delete_local"
	// (the file was deleted server-side and the client should remove its
	// local copy).
	Action string
	// RemoteVersion is the version at which this change happened.
	RemoteVersion int64
}

// Action values for SyncAction.Action.
const (
	ActionDownload    = "download"
	ActionDeleteLocal = "delete_local"
)

// SyncService computes the server's half of a manifest diff (REQUIREMENTS.md
// §6.3/§6.6): given a client's last-synced version, it reports everything
// that changed for that user since then.
//
// Scope boundary (intentional, matches the task's instructions): this phase
// implements only the server-authoritative half of the protocol described in
// §6.6. It does not accept or compare a client-submitted manifest
// (name/size/mtime/sha256 per file), so it can only ever emit "download" (new
// or modified server-side) or "delete_local" (removed server-side) actions —
// never "upload" or "conflict". Genuine bidirectional conflict detection
// (§6.2's "exists, hash differs" case and §6.5's resolution options) requires
// the client to send its local manifest so the server can compare hashes
// per-file; §6.6 itself frames that request field as part of the protocol,
// but wiring it up is out of scope here and left for a future refinement
// rather than inventing an unspecified conflict-detection scheme now.
type SyncService struct {
	files repo.FileRepo
}

// NewSyncService constructs a SyncService backed by files.
func NewSyncService(files repo.FileRepo) *SyncService {
	return &SyncService{files: files}
}

// Diff returns every change for uploadedBy since sinceVersion, as a list of
// client-actionable SyncActions, plus the new version the client should
// remember as its lastSyncedVersion for the next round.
func (s *SyncService) Diff(ctx context.Context, uploadedBy, sinceVersion int64) (actions []SyncAction, newVersion int64, err error) {
	changed, deleted, currentVersion, err := s.files.ListChangedSince(ctx, uploadedBy, sinceVersion)
	if err != nil {
		return nil, 0, fmt.Errorf("service: sync diff: %w", err)
	}

	actions = make([]SyncAction, 0, len(changed)+len(deleted))
	for _, f := range changed {
		actions = append(actions, SyncAction{
			Name:          f.Name,
			Action:        ActionDownload,
			RemoteVersion: f.Version,
		})
	}
	for _, d := range deleted {
		actions = append(actions, SyncAction{
			Name:          d.Name,
			Action:        ActionDeleteLocal,
			RemoteVersion: d.Version,
		})
	}

	return actions, currentVersion, nil
}
