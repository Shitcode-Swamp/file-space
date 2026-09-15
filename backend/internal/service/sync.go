package service

import (
	"context"
	"fmt"
	"time"

	"filespace/backend/internal/repo"
)

// SyncAction describes one action a client should take to bring its local
// copy of a user's folder back in line with the server, per
// REQUIREMENTS.md §6.6's response shape.
type SyncAction struct {
	// Name is the file's name.
	Name string
	// Action is "download" (the file is new or was modified server-side and
	// the client should fetch its current content), "delete_local" (the
	// file was deleted server-side and the client should remove its local
	// copy), or "conflict" (both sides changed since the client's last sync
	// — see the Remote* fields below and REQUIREMENTS.md §6.5).
	Action string
	// RemoteVersion is the version at which this change happened.
	RemoteVersion int64
	// Remote* are only populated when Action == ActionConflict — everything
	// a conflict-resolution UI needs to show the incoming server-side
	// version alongside the client's own local one, without a second
	// round-trip.
	RemoteSize       int64
	RemoteModifiedAt time.Time
	// RemoteEditedBy is the user id that last edited the remote version;
	// callers (the handler layer) resolve it to a username the same way
	// FileHandler's usernameResolver does for regular file listings.
	RemoteEditedBy int64
}

// Action values for SyncAction.Action.
const (
	ActionDownload    = "download"
	ActionDeleteLocal = "delete_local"
	ActionConflict    = "conflict"
)

// ManifestEntry is the server-side-relevant slice of a client's local
// manifest entry (REQUIREMENTS.md §6.1: "{name, size, mtime, sha256}") —
// only Name and SHA256 matter for conflict detection, so that's all Diff
// asks callers for; the handler layer decodes the full wire shape and maps
// down to this.
type ManifestEntry struct {
	Name   string
	SHA256 string
}

// SyncService computes the server's half of a manifest diff (REQUIREMENTS.md
// §6.3/§6.6): given a client's last-synced version (and, optionally, its
// current local manifest), it reports everything that changed for that user
// since then — including genuine conflicts when both sides changed the same
// file.
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
//
// manifest is the client's current local file list (REQUIREMENTS.md §6.1),
// variadic so a caller with nothing to compare against (or that predates
// this capability) can simply omit it and get the old download/delete_local
// -only behavior. When present, a server-side change to a name the manifest
// also lists is reported as "conflict" instead of "download" if the two
// sides' hashes actually differ — a name in the manifest whose hash matches
// the server's already has the right content and doesn't need any action at
// all, so it's silently dropped from the response rather than reported as a
// no-op "download". A file on either side with no hash yet (SHA256 == "",
// e.g. a row from before migrations/0004_file_sha256) is never treated as a
// conflict — there's not enough information to call one, so it falls back
// to the safe "download" default.
func (s *SyncService) Diff(ctx context.Context, uploadedBy, sinceVersion int64, manifest ...ManifestEntry) (actions []SyncAction, newVersion int64, err error) {
	changed, deleted, currentVersion, err := s.files.ListChangedSince(ctx, uploadedBy, sinceVersion)
	if err != nil {
		return nil, 0, fmt.Errorf("service: sync diff: %w", err)
	}

	localHashByName := make(map[string]string, len(manifest))
	for _, m := range manifest {
		localHashByName[m.Name] = m.SHA256
	}

	actions = make([]SyncAction, 0, len(changed)+len(deleted))
	for _, f := range changed {
		if localHash, ok := localHashByName[f.Name]; ok && localHash != "" && f.SHA256 != "" {
			if localHash == f.SHA256 {
				// Client already has exactly this content; nothing to do.
				continue
			}
			actions = append(actions, SyncAction{
				Name:             f.Name,
				Action:           ActionConflict,
				RemoteVersion:    f.Version,
				RemoteSize:       f.Size,
				RemoteModifiedAt: f.ModifiedAt,
				RemoteEditedBy:   f.EditedBy,
			})
			continue
		}
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
