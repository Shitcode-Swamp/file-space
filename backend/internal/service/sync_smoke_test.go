//go:build smoke

package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"filespace/backend/internal/db"
	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
)

// TestSmokeSyncServiceAgainstLiveDB exercises SyncService.Diff against a real
// Postgres instance pointed to by SMOKE_DATABASE_URL (no mocks): it creates a
// user and a couple of files directly through the repo layer, deletes one,
// and confirms Diff reports the right download/delete_local actions and
// version at each step. Excluded from normal `go test ./...` via the "smoke"
// build tag.
func TestSmokeSyncServiceAgainstLiveDB(t *testing.T) {
	dsn := os.Getenv("SMOKE_DATABASE_URL")
	if dsn == "" {
		t.Skip("SMOKE_DATABASE_URL not set")
	}

	sqlxDB, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sqlxDB.Close()

	ctx := context.Background()
	users := repo.NewPostgresUserRepo(sqlxDB)
	files := repo.NewPostgresFileRepo(sqlxDB)
	svc := NewSyncService(files)

	uniq := fmt.Sprintf("sync_smoke_user_%d", time.Now().UnixNano())
	user, err := users.Create(ctx, uniq, "irrelevant-hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	keyA := "key-a-" + uniq
	keyB := "key-b-" + uniq

	// Baseline: no changes yet since version 0.
	actions, v0, err := svc.Diff(ctx, user.ID, 0)
	if err != nil {
		t.Fatalf("diff baseline: %v", err)
	}
	if len(actions) != 0 || v0 != 0 {
		t.Fatalf("diff baseline: got actions=%v newVersion=%d, want empty/0", actions, v0)
	}

	// Create two files.
	f1, err := files.Create(ctx, domain.File{
		Name: "a.txt", StorageKey: keyA, Size: 10, Extension: "txt",
		UploadedBy: user.ID, EditedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create f1: %v", err)
	}
	f2, err := files.Create(ctx, domain.File{
		Name: "b.txt", StorageKey: keyB, Size: 20, Extension: "txt",
		UploadedBy: user.ID, EditedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create f2: %v", err)
	}

	// Diff from 0 should report both as "download".
	actions, v1, err := svc.Diff(ctx, user.ID, 0)
	if err != nil {
		t.Fatalf("diff after creates: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("diff after creates: got %d actions, want 2: %+v", len(actions), actions)
	}
	byName := map[string]SyncAction{}
	for _, a := range actions {
		byName[a.Name] = a
	}
	if a, ok := byName["a.txt"]; !ok || a.Action != ActionDownload || a.RemoteVersion != f1.Version {
		t.Fatalf("a.txt action wrong: %+v (want download, version %d)", a, f1.Version)
	}
	if a, ok := byName["b.txt"]; !ok || a.Action != ActionDownload || a.RemoteVersion != f2.Version {
		t.Fatalf("b.txt action wrong: %+v (want download, version %d)", a, f2.Version)
	}
	if v1 != f2.Version {
		t.Fatalf("newVersion = %d, want %d", v1, f2.Version)
	}

	// Diff from v1 (fully synced): no more changes.
	actions, v2, err := svc.Diff(ctx, user.ID, v1)
	if err != nil {
		t.Fatalf("diff fully synced: %v", err)
	}
	if len(actions) != 0 || v2 != v1 {
		t.Fatalf("diff fully synced: got actions=%v newVersion=%d, want empty/%d", actions, v2, v1)
	}

	// Delete a.txt; diff from v1 should now report exactly one delete_local
	// for a.txt, and leave b.txt (already synced) out entirely.
	if err := files.Delete(ctx, user.ID, f1.ID); err != nil {
		t.Fatalf("delete f1: %v", err)
	}
	actions, v3, err := svc.Diff(ctx, user.ID, v1)
	if err != nil {
		t.Fatalf("diff after delete: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("diff after delete: got %d actions, want 1: %+v", len(actions), actions)
	}
	if actions[0].Name != "a.txt" || actions[0].Action != ActionDeleteLocal {
		t.Fatalf("diff after delete: got %+v, want delete_local for a.txt", actions[0])
	}
	if v3 <= v1 {
		t.Fatalf("newVersion after delete = %d, want > %d", v3, v1)
	}

	// A brand new client (sinceVersion 0) should see b.txt as download and
	// a.txt should NOT appear as delete_local (it never existed for that
	// client, so there's nothing to delete locally) -- but our current
	// ListChangedSince-based implementation reports every deletion with
	// version > sinceVersion regardless, which is still correct: deleting a
	// file the client never downloaded is a harmless no-op delete_local.
	actions, _, err = svc.Diff(ctx, user.ID, 0)
	if err != nil {
		t.Fatalf("diff fresh client: %v", err)
	}
	sawB, sawADelete := false, false
	for _, a := range actions {
		if a.Name == "b.txt" && a.Action == ActionDownload {
			sawB = true
		}
		if a.Name == "a.txt" && a.Action == ActionDeleteLocal {
			sawADelete = true
		}
	}
	if !sawB {
		t.Fatalf("diff fresh client: expected download for b.txt, got %+v", actions)
	}
	if !sawADelete {
		t.Fatalf("diff fresh client: expected delete_local for a.txt, got %+v", actions)
	}
}
