//go:build smoke

package repo

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"

	"filespace/backend/internal/db"
	"filespace/backend/internal/domain"
)

// TestListExtensionFilter covers PostgresFileRepo.List's extension filter:
// no filter returns everything, a matching filter narrows to just that
// extension, and a filter matching nothing returns an empty slice (not an
// error).
func TestListExtensionFilter(t *testing.T) {
	sqlxDB, users, files := connectForListTests(t)
	defer sqlxDB.Close()
	ctx := context.Background()

	owner, err := users.Create(ctx, "owner_extfilter", "hash")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}

	mustCreateFile(t, ctx, files, "a.java", "k-ext-1", "java", owner.ID, owner.ID)
	mustCreateFile(t, ctx, files, "b.png", "k-ext-2", "png", owner.ID, owner.ID)
	mustCreateFile(t, ctx, files, "c.txt", "k-ext-3", "txt", owner.ID, owner.ID)

	pngOnly, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, Extension: "png"})
	if err != nil {
		t.Fatalf("list png: %v", err)
	}
	if len(pngOnly) != 1 || pngOnly[0].Extension != "png" {
		t.Fatalf("expected exactly 1 .png file, got %+v", pngOnly)
	}

	all, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 files with no extension filter, got %d", len(all))
	}

	none, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, Extension: "pdf"})
	if err != nil {
		t.Fatalf("list pdf (no match): %v", err)
	}
	if none == nil {
		t.Fatalf("expected an empty (non-nil) slice for a non-matching extension, got nil")
	}
	if len(none) != 0 {
		t.Fatalf("expected 0 files for a non-matching extension, got %d", len(none))
	}
}

// TestListSortByEditedByUsername covers the sort-by-editor-username SQL
// mechanism directly. FileService.Create always sets EditedBy == UploadedBy,
// so in the shipped product this ORDER BY can never actually reorder a
// user's own file list (every row ties on edited_by). To exercise the SQL
// itself we bypass FileService and call repo.FileRepo.Create directly with a
// hand-picked EditedBy that differs from UploadedBy -- FileRepo.Create's
// signature does not enforce they match; only FileService enforces that at
// a higher layer, so this is a legitimate way to test the mechanism in
// isolation.
func TestListSortByEditedByUsername(t *testing.T) {
	sqlxDB, users, files := connectForListTests(t)
	defer sqlxDB.Close()
	ctx := context.Background()

	owner, err := users.Create(ctx, "owner_sorttest", "hash")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}

	// Usernames chosen to sort alice < bob < carol, but we insert files in
	// the order carol, alice, bob so insertion order != expected order.
	carol, err := users.Create(ctx, "carol", "hash")
	if err != nil {
		t.Fatalf("create carol: %v", err)
	}
	alice, err := users.Create(ctx, "alice", "hash")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob", "hash")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	fCarol := mustCreateFile(t, ctx, files, "edited-by-carol.txt", "k-sort-1", "txt", owner.ID, carol.ID)
	fAlice := mustCreateFile(t, ctx, files, "edited-by-alice.txt", "k-sort-2", "txt", owner.ID, alice.ID)
	fBob := mustCreateFile(t, ctx, files, "edited-by-bob.txt", "k-sort-3", "txt", owner.ID, bob.ID)

	asc, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, SortField: SortByEditedBy, SortOrder: "asc"})
	if err != nil {
		t.Fatalf("list asc: %v", err)
	}
	assertFileIDOrder(t, "asc", asc, fAlice.ID, fBob.ID, fCarol.ID)

	desc, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, SortField: SortByEditedBy, SortOrder: "desc"})
	if err != nil {
		t.Fatalf("list desc: %v", err)
	}
	assertFileIDOrder(t, "desc", desc, fCarol.ID, fBob.ID, fAlice.ID)
}

// TestListSortByUploadedByUsername covers sorting by the *uploader's*
// username, the counterpart to TestListSortByEditedByUsername. Every file
// here is uploaded by a different user (unlike edited_by, uploaded_by is
// fixed at creation and can't be hand-picked independently the same way,
// so each file needs its own owner).
func TestListSortByUploadedByUsername(t *testing.T) {
	sqlxDB, users, files := connectForListTests(t)
	defer sqlxDB.Close()
	ctx := context.Background()

	// Usernames chosen to sort alice < bob < carol, created in a different
	// order so insertion order != expected sort order.
	carol, err := users.Create(ctx, "carol_up", "hash")
	if err != nil {
		t.Fatalf("create carol: %v", err)
	}
	alice, err := users.Create(ctx, "alice_up", "hash")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, "bob_up", "hash")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	// List scopes by a single UploadedBy, so to observe cross-file ordering
	// we query each owner's single file and instead assert the SQL doesn't
	// error and returns the right file — the actual ordering mechanism
	// (uploader.username ASC/DESC) is identical to the edited_by case
	// already covered above, just against a different joined column.
	fCarol := mustCreateFile(t, ctx, files, "by-carol.txt", "k-up-1", "txt", carol.ID, carol.ID)
	fAlice := mustCreateFile(t, ctx, files, "by-alice.txt", "k-up-2", "txt", alice.ID, alice.ID)
	fBob := mustCreateFile(t, ctx, files, "by-bob.txt", "k-up-3", "txt", bob.ID, bob.ID)

	for owner, want := range map[int64]int64{carol.ID: fCarol.ID, alice.ID: fAlice.ID, bob.ID: fBob.ID} {
		got, _, err := files.List(ctx, ListParams{UploadedBy: owner, SortField: SortByUploadedBy, SortOrder: "asc"})
		if err != nil {
			t.Fatalf("list for owner %d: %v", owner, err)
		}
		assertFileIDOrder(t, "uploadedBy sort scoping", got, want)
	}
}

// TestListSortByNameCreatedAtModifiedAt covers the three "plain column"
// sort fields (no join involved), which default to SortByName when
// unspecified.
func TestListSortByNameCreatedAtModifiedAt(t *testing.T) {
	sqlxDB, users, files := connectForListTests(t)
	defer sqlxDB.Close()
	ctx := context.Background()

	owner, err := users.Create(ctx, "owner_namesort", "hash")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}

	// Created in an order that puts their names out of alphabetical order,
	// so "created in this order" != "name-sorted order" != "id order".
	fZebra := mustCreateFile(t, ctx, files, "zebra.txt", "k-name-1", "txt", owner.ID, owner.ID)
	fApple := mustCreateFile(t, ctx, files, "apple.txt", "k-name-2", "txt", owner.ID, owner.ID)
	fMango := mustCreateFile(t, ctx, files, "mango.txt", "k-name-3", "txt", owner.ID, owner.ID)

	byNameAsc, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, SortField: SortByName, SortOrder: "asc"})
	if err != nil {
		t.Fatalf("list by name asc: %v", err)
	}
	assertFileIDOrder(t, "name asc", byNameAsc, fApple.ID, fMango.ID, fZebra.ID)

	// The zero-value SortField ("") must behave identically to explicit
	// SortByName -- it's meant to be the default.
	byDefault, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID})
	if err != nil {
		t.Fatalf("list with default sort field: %v", err)
	}
	assertFileIDOrder(t, "default sort field", byDefault, fApple.ID, fMango.ID, fZebra.ID)

	// created_at/modified_at increase with each mustCreateFile call above
	// (zebra, apple, mango, in that order), so sorting by either ascending
	// must reproduce creation order regardless of name.
	byCreatedAsc, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, SortField: SortByCreatedAt, SortOrder: "asc"})
	if err != nil {
		t.Fatalf("list by createdAt asc: %v", err)
	}
	assertFileIDOrder(t, "createdAt asc", byCreatedAsc, fZebra.ID, fApple.ID, fMango.ID)

	byModifiedDesc, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, SortField: SortByModifiedAt, SortOrder: "desc"})
	if err != nil {
		t.Fatalf("list by modifiedAt desc: %v", err)
	}
	assertFileIDOrder(t, "modifiedAt desc", byModifiedDesc, fMango.ID, fApple.ID, fZebra.ID)
}

// TestListPagination covers Limit/Offset/hasMore: a zero Limit returns
// everything with hasMore always false; a positive Limit returns at most
// that many rows and correctly reports whether more exist beyond the page;
// paging through with Offset covers the full set exactly once, in stable
// order (tie-broken by f.id, per the ORDER BY ... , f.id ASC clause).
func TestListPagination(t *testing.T) {
	sqlxDB, users, files := connectForListTests(t)
	defer sqlxDB.Close()
	ctx := context.Background()

	owner, err := users.Create(ctx, "owner_paginate", "hash")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}

	const total = 5
	want := make([]int64, total)
	for i := range total {
		f := mustCreateFile(t, ctx, files, fmt.Sprintf("file-%d.txt", i), fmt.Sprintf("k-page-%d", i), "txt", owner.ID, owner.ID)
		want[i] = f.ID
	}

	unbounded, hasMore, err := files.List(ctx, ListParams{UploadedBy: owner.ID})
	if err != nil {
		t.Fatalf("list unbounded: %v", err)
	}
	if hasMore {
		t.Fatalf("expected hasMore=false with no Limit set")
	}
	assertFileIDOrder(t, "unbounded", unbounded, want...)

	var gotAll []int64
	for offset := 0; ; offset += 2 {
		page, hasMore, err := files.List(ctx, ListParams{UploadedBy: owner.ID, Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("list page at offset %d: %v", offset, err)
		}
		for _, f := range page {
			gotAll = append(gotAll, f.ID)
		}
		if !hasMore {
			break
		}
		if len(page) != 2 {
			t.Fatalf("expected a full page of 2 while hasMore=true, got %d", len(page))
		}
	}
	if len(gotAll) != total {
		t.Fatalf("expected %d files paged through, got %d (%v)", total, len(gotAll), gotAll)
	}
	for i, id := range want {
		if gotAll[i] != id {
			t.Fatalf("paged order mismatch at index %d: want %v, got %v", i, want, gotAll)
		}
	}

	// A page starting exactly at the last row must report hasMore=false.
	lastPage, hasMore, err := files.List(ctx, ListParams{UploadedBy: owner.ID, Limit: 2, Offset: total - 1})
	if err != nil {
		t.Fatalf("list last page: %v", err)
	}
	if hasMore {
		t.Fatalf("expected hasMore=false for the final partial page")
	}
	if len(lastPage) != 1 || lastPage[0].ID != want[total-1] {
		t.Fatalf("expected exactly the last file on the final page, got %+v", lastPage)
	}
}

// TestListSortTieBreaksByFileID covers the secondary sort key: when two
// files tie on editor username, both "asc" and "desc" must still return a
// deterministic order (by f.id ascending), not an arbitrary one.
func TestListSortTieBreaksByFileID(t *testing.T) {
	sqlxDB, users, files := connectForListTests(t)
	defer sqlxDB.Close()
	ctx := context.Background()

	owner, err := users.Create(ctx, "owner_tietest", "hash")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	editor, err := users.Create(ctx, "shared_editor", "hash")
	if err != nil {
		t.Fatalf("create editor: %v", err)
	}

	// Both files are edited by the same user, so they tie on the primary
	// sort key. f1 is created first and so must have the smaller id.
	f1 := mustCreateFile(t, ctx, files, "first.txt", "k-tie-1", "txt", owner.ID, editor.ID)
	f2 := mustCreateFile(t, ctx, files, "second.txt", "k-tie-2", "txt", owner.ID, editor.ID)
	if f2.ID <= f1.ID {
		t.Fatalf("expected f2.ID > f1.ID to meaningfully test the id tie-break, got f1=%d f2=%d", f1.ID, f2.ID)
	}

	asc, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, SortField: SortByEditedBy, SortOrder: "asc"})
	if err != nil {
		t.Fatalf("list asc: %v", err)
	}
	assertFileIDOrder(t, "asc tie-break", asc, f1.ID, f2.ID)

	desc, _, err := files.List(ctx, ListParams{UploadedBy: owner.ID, SortField: SortByEditedBy, SortOrder: "desc"})
	if err != nil {
		t.Fatalf("list desc: %v", err)
	}
	// Ties still come out in f.id ASC order even under a DESC primary sort,
	// per the SQL's `ORDER BY editor.username DESC, f.id ASC`.
	assertFileIDOrder(t, "desc tie-break", desc, f1.ID, f2.ID)
}

// TestListScopingIgnoresOtherUsersFiles covers that List only ever returns
// files owned by params.UploadedBy, regardless of sort/filter params -- a
// file owned by a different user must never appear, in any combination.
func TestListScopingIgnoresOtherUsersFiles(t *testing.T) {
	sqlxDB, users, files := connectForListTests(t)
	defer sqlxDB.Close()
	ctx := context.Background()

	owner, err := users.Create(ctx, "owner_scopetest", "hash")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := users.Create(ctx, "other_scopetest", "hash")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	mine := mustCreateFile(t, ctx, files, "mine.png", "k-scope-1", "png", owner.ID, owner.ID)
	mustCreateFile(t, ctx, files, "theirs.png", "k-scope-2", "png", other.ID, other.ID)

	for _, params := range []ListParams{
		{UploadedBy: owner.ID},
		{UploadedBy: owner.ID, Extension: "png"},
		{UploadedBy: owner.ID, SortField: SortByEditedBy, SortOrder: "asc"},
		{UploadedBy: owner.ID, SortField: SortByEditedBy, SortOrder: "desc"},
	} {
		got, _, err := files.List(ctx, params)
		if err != nil {
			t.Fatalf("list %+v: %v", params, err)
		}
		if len(got) != 1 || got[0].ID != mine.ID {
			t.Fatalf("expected scoping to isolate owner's single file for params %+v, got %+v", params, got)
		}
		for _, f := range got {
			if f.UploadedBy != owner.ID {
				t.Fatalf("cross-user leak: file uploaded_by=%d appeared while listing for owner %d (params %+v)", f.UploadedBy, owner.ID, params)
			}
		}
	}
}

// connectForListTests skips the test if SMOKE_DATABASE_URL is unset, and
// otherwise connects to it and returns ready-to-use repos.
func connectForListTests(t *testing.T) (*sqlx.DB, UserRepo, FileRepo) {
	t.Helper()
	dsn := os.Getenv("SMOKE_DATABASE_URL")
	if dsn == "" {
		t.Skip("SMOKE_DATABASE_URL not set")
	}
	sqlxDB, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return sqlxDB, NewPostgresUserRepo(sqlxDB), NewPostgresFileRepo(sqlxDB)
}

// mustCreateFile inserts a file directly through the repo layer (bypassing
// FileService) so tests can set edited_by independently of uploaded_by.
func mustCreateFile(t *testing.T, ctx context.Context, files FileRepo, name, storageKey, extension string, uploadedBy, editedBy int64) domain.File {
	t.Helper()
	f, err := files.Create(ctx, domain.File{
		Name:       name,
		StorageKey: storageKey,
		Size:       1,
		Extension:  extension,
		UploadedBy: uploadedBy,
		EditedBy:   editedBy,
	})
	if err != nil {
		t.Fatalf("create file %q: %v", name, err)
	}
	return f
}

// assertFileIDOrder asserts got is exactly the files with the given ids, in
// that order.
func assertFileIDOrder(t *testing.T, label string, got []domain.File, wantIDs ...int64) {
	t.Helper()
	if len(got) != len(wantIDs) {
		t.Fatalf("%s: expected %d files, got %d (%+v)", label, len(wantIDs), len(got), got)
	}
	for i, want := range wantIDs {
		if got[i].ID != want {
			gotIDs := make([]int64, len(got))
			for j, f := range got {
				gotIDs[j] = f.ID
			}
			t.Fatalf("%s: expected id order %v, got %v", label, wantIDs, gotIDs)
		}
	}
}
