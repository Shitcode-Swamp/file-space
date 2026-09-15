//go:build smoke

package repo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"filespace/backend/internal/db"
	"filespace/backend/internal/domain"
)

// TestSmokeAgainstLiveDB exercises UserRepo and FileRepo against a real
// Postgres instance pointed to by SMOKE_DATABASE_URL. It is excluded from
// normal `go test ./...` runs via the "smoke" build tag, and normal `go
// vet ./...` (which does not require a live DB) is unaffected.
func TestSmokeAgainstLiveDB(t *testing.T) {
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
	users := NewPostgresUserRepo(sqlxDB)
	files := NewPostgresFileRepo(sqlxDB)

	// --- users ---
	alice, err := users.Create(ctx, "alice_smoke", "hash-a")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if alice.ID == 0 {
		t.Fatalf("expected alice to get an id")
	}
	bob, err := users.Create(ctx, "bob_smoke", "hash-b")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	got, err := users.GetByUsername(ctx, "alice_smoke")
	if err != nil || got.ID != alice.ID {
		t.Fatalf("GetByUsername mismatch: %+v, err=%v", got, err)
	}
	got2, err := users.GetByID(ctx, alice.ID)
	if err != nil || got2.Username != "alice_smoke" {
		t.Fatalf("GetByID mismatch: %+v, err=%v", got2, err)
	}
	if _, err := users.GetByUsername(ctx, "nobody_smoke"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// --- files: create ---
	f1, err := files.Create(ctx, domain.File{
		Name: "Program.cs", StorageKey: "k-1-smoke", Size: 10,
		Extension: "cs", UploadedBy: alice.ID, EditedBy: bob.ID,
	})
	if err != nil {
		t.Fatalf("create f1: %v", err)
	}
	if f1.ID == 0 || f1.Version == 0 || f1.CreatedAt.IsZero() {
		t.Fatalf("expected DB-assigned id/version/timestamps, got %+v", f1)
	}

	f2, err := files.Create(ctx, domain.File{
		Name: "image.jpg", StorageKey: "k-2-smoke", Size: 20,
		Extension: "jpg", UploadedBy: alice.ID, EditedBy: alice.ID,
	})
	if err != nil {
		t.Fatalf("create f2: %v", err)
	}
	if f2.Version <= f1.Version {
		t.Fatalf("expected f2.Version > f1.Version, got f1=%d f2=%d", f1.Version, f2.Version)
	}

	// A file owned by bob must never surface in alice's queries.
	_, err = files.Create(ctx, domain.File{
		Name: "secret.txt", StorageKey: "k-3-smoke", Size: 5,
		Extension: "txt", UploadedBy: bob.ID, EditedBy: bob.ID,
	})
	if err != nil {
		t.Fatalf("create bob file: %v", err)
	}

	// --- List: scoping + extension filter + sort by editor username ---
	all, _, err := files.List(ctx, ListParams{UploadedBy: alice.ID})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 files for alice, got %d", len(all))
	}
	for _, f := range all {
		if f.UploadedBy != alice.ID {
			t.Fatalf("cross-user leak: got file uploaded_by=%d while listing for alice", f.UploadedBy)
		}
	}

	csOnly, _, err := files.List(ctx, ListParams{UploadedBy: alice.ID, Extension: "cs"})
	if err != nil {
		t.Fatalf("list cs: %v", err)
	}
	if len(csOnly) != 1 || csOnly[0].Extension != "cs" {
		t.Fatalf("expected exactly 1 .cs file, got %+v", csOnly)
	}

	ascByEditor, _, err := files.List(ctx, ListParams{UploadedBy: alice.ID, SortField: SortByEditedBy, SortOrder: "asc"})
	if err != nil {
		t.Fatalf("list sorted asc: %v", err)
	}
	if len(ascByEditor) != 2 {
		t.Fatalf("expected 2 files sorted, got %d", len(ascByEditor))
	}
	// f1 is edited by bob ("bob_smoke"), f2 by alice ("alice_smoke") -> alice sorts first ascending.
	if ascByEditor[0].ID != f2.ID || ascByEditor[1].ID != f1.ID {
		t.Fatalf("expected ascending sort by editor username to put f2 (alice) before f1 (bob), got order %d,%d", ascByEditor[0].ID, ascByEditor[1].ID)
	}

	// --- GetByID scoping ---
	if _, err := files.GetByID(ctx, bob.ID, f1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound when bob fetches alice's file, got %v", err)
	}
	fetched, err := files.GetByID(ctx, alice.ID, f1.ID)
	if err != nil || fetched.ID != f1.ID {
		t.Fatalf("GetByID mismatch: %+v, err=%v", fetched, err)
	}

	// --- ListChangedSince before any deletion ---
	changed, deleted, curVersion, err := files.ListChangedSince(ctx, alice.ID, 0)
	if err != nil {
		t.Fatalf("ListChangedSince: %v", err)
	}
	if len(changed) != 2 || len(deleted) != 0 {
		t.Fatalf("expected 2 changed, 0 deleted, got %d/%d", len(changed), len(deleted))
	}
	if curVersion != f2.Version {
		t.Fatalf("expected currentVersion == f2.Version(%d), got %d", f2.Version, curVersion)
	}

	// no-op: nothing changed since curVersion
	changed2, deleted2, curVersion2, err := files.ListChangedSince(ctx, alice.ID, curVersion)
	if err != nil {
		t.Fatalf("ListChangedSince no-op: %v", err)
	}
	if len(changed2) != 0 || len(deleted2) != 0 || curVersion2 != curVersion {
		t.Fatalf("expected no-op, got changed=%d deleted=%d version=%d", len(changed2), len(deleted2), curVersion2)
	}

	// --- Delete: cross-user delete must fail, must not leak/alter data ---
	if err := files.Delete(ctx, bob.ID, f1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting another user's file, got %v", err)
	}
	if _, err := files.GetByID(ctx, alice.ID, f1.ID); err != nil {
		t.Fatalf("file should still exist after failed cross-user delete: %v", err)
	}

	if err := files.Delete(ctx, alice.ID, f1.ID); err != nil {
		t.Fatalf("delete f1: %v", err)
	}
	if _, err := files.GetByID(ctx, alice.ID, f1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected f1 gone after delete, got %v", err)
	}

	// --- ListChangedSince must report the deletion as a tombstone ---
	changed3, deleted3, curVersion3, err := files.ListChangedSince(ctx, alice.ID, curVersion)
	if err != nil {
		t.Fatalf("ListChangedSince after delete: %v", err)
	}
	if len(changed3) != 0 {
		t.Fatalf("expected 0 changed files after delete-only change, got %d", len(changed3))
	}
	if len(deleted3) != 1 || deleted3[0].FileID != f1.ID || deleted3[0].Name != "Program.cs" || strings.TrimSpace(deleted3[0].StorageKey) != "k-1-smoke" {
		t.Fatalf("expected 1 tombstone for f1, got %+v", deleted3)
	}
	if curVersion3 <= curVersion {
		t.Fatalf("expected currentVersion to advance past the deletion, got %d (was %d)", curVersion3, curVersion)
	}

	t.Log("smoke test passed against live Postgres")
}
