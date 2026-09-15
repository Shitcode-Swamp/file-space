package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/authctx"
	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/service"
)

// This file runs under plain `go test ./...` (no build tag, no live
// Postgres). It exercises POST /api/sync/diff's HTTP-level concerns that
// sit above SyncService.Diff itself (already covered thoroughly by
// internal/service/sync_diff_test.go): decoding a client-submitted
// manifest out of the request body, and resolving a conflict action's
// RemoteEditedBy id to a username in the response, the same way
// FileHandler already does for uploadedBy/editedBy.

// singleChangeFileRepo is a minimal repo.FileRepo that always reports one
// fixed domain.File as "changed since" any version below its own, and no
// deletions -- just enough to drive SyncService.Diff through the handler
// without a live Postgres. Everything else panics if called; this fake
// exists solely for this file's conflict-response test.
type singleChangeFileRepo struct {
	file domain.File
}

var _ repo.FileRepo = (*singleChangeFileRepo)(nil)

func (r *singleChangeFileRepo) List(ctx context.Context, params repo.ListParams) ([]domain.File, bool, error) {
	return nil, false, nil
}

func (r *singleChangeFileRepo) GetByID(ctx context.Context, uploadedBy, id int64) (domain.File, error) {
	return domain.File{}, repo.ErrNotFound
}

func (r *singleChangeFileRepo) Create(ctx context.Context, f domain.File) (domain.File, error) {
	return domain.File{}, errors.New("singleChangeFileRepo: Create not implemented")
}

func (r *singleChangeFileRepo) Delete(ctx context.Context, uploadedBy, id int64) error {
	return errors.New("singleChangeFileRepo: Delete not implemented")
}

func (r *singleChangeFileRepo) ListChangedSince(ctx context.Context, uploadedBy, sinceVersion int64) ([]domain.File, []domain.Deletion, int64, error) {
	if sinceVersion >= r.file.Version {
		return nil, nil, sinceVersion, nil
	}
	return []domain.File{r.file}, nil, r.file.Version, nil
}

// TestDiffHTTP_ConflictResponseIncludesResolvedUsername drives the real
// handler (behind a fixed-user-id stand-in for JWTAuth) with a manifest
// whose hash deliberately mismatches the one "server-side" file, and checks
// the JSON response: action "conflict", and a conflict.remoteEditedBy
// *username* (not a numeric id) resolved via UserRepo.
func TestDiffHTTP_ConflictResponseIncludesResolvedUsername(t *testing.T) {
	const ownerID, editorID = int64(1), int64(2)
	modTime := time.Date(2026, 9, 15, 9, 30, 0, 0, time.UTC)

	files := &singleChangeFileRepo{file: domain.File{
		Name: "budget.xlsx", UploadedBy: ownerID, EditedBy: editorID,
		Version: 7, Size: 2048, ModifiedAt: modTime, SHA256: "remote-hash",
	}}
	users := &fakeUserRepo{usernames: map[int64]string{editorID: "d.kim"}}
	syncHandler := NewSyncHandler(service.NewSyncService(files), users)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), ownerID)))
		})
	})
	r.Route("/api/sync", syncHandler.Routes)

	body, err := json.Marshal(map[string]any{
		"lastSyncedVersion": 0,
		"manifest": []map[string]any{
			{"name": "budget.xlsx", "size": 2048, "mtime": "2026-09-15T09:00:00Z", "sha256": "local-hash"},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sync/diff", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		NewVersion int64 `json:"newVersion"`
		Actions    []struct {
			Name          string `json:"name"`
			Action        string `json:"action"`
			RemoteVersion int64  `json:"remoteVersion"`
			Conflict      *struct {
				RemoteSize       int64     `json:"remoteSize"`
				RemoteModifiedAt time.Time `json:"remoteModifiedAt"`
				RemoteEditedBy   string    `json:"remoteEditedBy"`
			} `json:"conflict"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v, body=%s", err, rec.Body.String())
	}

	if len(resp.Actions) != 1 {
		t.Fatalf("actions = %+v, want exactly 1", resp.Actions)
	}
	got := resp.Actions[0]
	if got.Action != "conflict" || got.Name != "budget.xlsx" || got.RemoteVersion != 7 {
		t.Fatalf("action = %+v, want conflict for budget.xlsx at version 7", got)
	}
	if got.Conflict == nil {
		t.Fatalf("conflict details missing from response: %+v", got)
	}
	if got.Conflict.RemoteSize != 2048 || !got.Conflict.RemoteModifiedAt.Equal(modTime) {
		t.Fatalf("conflict details = %+v, want size=2048 modifiedAt=%v", got.Conflict, modTime)
	}
	if got.Conflict.RemoteEditedBy != "d.kim" {
		t.Fatalf("conflict.remoteEditedBy = %q, want the resolved username %q, not a numeric id", got.Conflict.RemoteEditedBy, "d.kim")
	}
}
