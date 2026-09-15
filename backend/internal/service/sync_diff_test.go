package service

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
)

// fakeSyncFileRepo is an in-memory repo.FileRepo built specifically to
// exercise SyncService.Diff without a live Postgres. It replicates the
// *observable* versioning behavior of PostgresFileRepo (see
// backend/internal/repo/file_repo.go and
// backend/migrations/0002_sync_versioning.up.sql):
//
//   - A single monotonically increasing counter backs both file creation and
//     file_deletions.version, exactly like the shared `files_version_seq`
//     Postgres sequence -- so a Delete that happens "between" two Creates in
//     real time will observably land its version between theirs too.
//   - Create assigns a fresh version to the new file.
//   - Delete removes the file and appends a tombstone (domain.Deletion) that
//     also consumes a fresh version from the same counter.
//   - ListChangedSince returns every file and every deletion belonging to
//     uploadedBy with version > sinceVersion, plus the max version observed
//     across both sets (or sinceVersion unchanged if nothing qualified).
//
// It is not a general-purpose FileRepo fake (List/GetByID are minimal, just
// enough to satisfy the interface) -- its only job is backing sync tests.
type fakeSyncFileRepo struct {
	mu        sync.Mutex
	seq       int64
	nextID    int64
	files     map[int64]domain.File
	deletions []fakeDeletion
}

// fakeDeletion pairs a domain.Deletion with the uploadedBy it belongs to, so
// ListChangedSince can scope deletions per-user the same way
// PostgresFileRepo does via the file_deletions.uploaded_by column
// (domain.Deletion itself carries no owner field, matching the real
// repo/handler layer, which never hands owner ids back to callers).
type fakeDeletion struct {
	domain.Deletion
	uploadedBy int64
}

func newFakeSyncFileRepo() *fakeSyncFileRepo {
	return &fakeSyncFileRepo{files: make(map[int64]domain.File)}
}

var _ repo.FileRepo = (*fakeSyncFileRepo)(nil)

func (f *fakeSyncFileRepo) List(_ context.Context, params repo.ListParams) ([]domain.File, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]domain.File, 0)
	for _, file := range f.files {
		if file.UploadedBy == params.UploadedBy {
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, false, nil
}

func (f *fakeSyncFileRepo) GetByID(_ context.Context, uploadedBy, id int64) (domain.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	file, ok := f.files[id]
	if !ok || file.UploadedBy != uploadedBy {
		return domain.File{}, repo.ErrNotFound
	}
	return file, nil
}

// Create assigns the new file a fresh id and a fresh version from the
// shared counter, mirroring the Postgres `DEFAULT nextval('files_version_seq')`
// column default.
func (f *fakeSyncFileRepo) Create(_ context.Context, file domain.File) (domain.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nextID++
	f.seq++
	file.ID = f.nextID
	file.Version = f.seq
	f.files[file.ID] = file
	return file, nil
}

// Delete removes the file (scoped to uploadedBy) and records a tombstone
// with a fresh version drawn from the SAME counter used by Create, exactly
// as PostgresFileRepo.Delete inserts into file_deletions with
// DEFAULT nextval('files_version_seq') in the same transaction that removes
// the row from files.
func (f *fakeSyncFileRepo) Delete(_ context.Context, uploadedBy, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	file, ok := f.files[id]
	if !ok || file.UploadedBy != uploadedBy {
		return repo.ErrNotFound
	}
	delete(f.files, id)

	f.seq++
	f.deletions = append(f.deletions, fakeDeletion{
		Deletion: domain.Deletion{
			FileID:     file.ID,
			Name:       file.Name,
			StorageKey: file.StorageKey,
			Version:    f.seq,
		},
		uploadedBy: uploadedBy,
	})
	return nil
}

// ListChangedSince mirrors PostgresFileRepo.ListChangedSince: files and
// deletions belonging to uploadedBy with version > sinceVersion, plus the
// max version observed (or sinceVersion unchanged if nothing qualified).
func (f *fakeSyncFileRepo) ListChangedSince(_ context.Context, uploadedBy, sinceVersion int64) ([]domain.File, []domain.Deletion, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	currentVersion := sinceVersion

	changed := make([]domain.File, 0)
	for _, file := range f.files {
		if file.UploadedBy == uploadedBy && file.Version > sinceVersion {
			changed = append(changed, file)
			if file.Version > currentVersion {
				currentVersion = file.Version
			}
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Version < changed[j].Version })

	deleted := make([]domain.Deletion, 0)
	for _, d := range f.deletions {
		if d.uploadedBy == uploadedBy && d.Version > sinceVersion {
			deleted = append(deleted, d.Deletion)
			if d.Version > currentVersion {
				currentVersion = d.Version
			}
		}
	}
	sort.Slice(deleted, func(i, j int) bool { return deleted[i].Version < deleted[j].Version })

	return changed, deleted, currentVersion, nil
}

// actionsByName indexes a slice of SyncAction by Name for convenient
// per-file assertions regardless of return order.
func actionsByName(actions []SyncAction) map[string]SyncAction {
	out := make(map[string]SyncAction, len(actions))
	for _, a := range actions {
		out[a.Name] = a
	}
	return out
}

// 1. Baseline: nothing created yet -> empty actions, newVersion unchanged.
func TestSyncServiceDiff_Baseline(t *testing.T) {
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	actions, newVersion, err := svc.Diff(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("actions = %+v, want empty", actions)
	}
	if newVersion != 0 {
		t.Fatalf("newVersion = %d, want 0", newVersion)
	}
}

// 2. One file created -> exactly one "download" action for it, and
// newVersion equals that file's version.
func TestSyncServiceDiff_OneFileCreated(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const owner = int64(1)
	f, err := files.Create(ctx, domain.File{Name: "a.txt", StorageKey: "key-a", UploadedBy: owner, EditedBy: owner})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	actions, newVersion, err := svc.Diff(ctx, owner, 0)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions = %+v, want exactly 1", actions)
	}
	got := actions[0]
	if got.Name != "a.txt" || got.Action != ActionDownload || got.RemoteVersion != f.Version {
		t.Fatalf("action = %+v, want {a.txt download %d}", got, f.Version)
	}
	if newVersion != f.Version {
		t.Fatalf("newVersion = %d, want %d", newVersion, f.Version)
	}
}

// 3. Multiple files created between two Diff calls all show up as
// "download" actions in a single Diff call -- not just the most recent one
// -- each carrying its own correct RemoteVersion.
func TestSyncServiceDiff_MultipleFilesCreated_AllReported(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const owner = int64(1)

	// First Diff call: nothing yet.
	if actions, v, err := svc.Diff(ctx, owner, 0); err != nil || len(actions) != 0 || v != 0 {
		t.Fatalf("baseline diff: actions=%+v v=%d err=%v", actions, v, err)
	}

	f1, err := files.Create(ctx, domain.File{Name: "a.txt", StorageKey: "key-a", UploadedBy: owner, EditedBy: owner})
	if err != nil {
		t.Fatalf("create a.txt: %v", err)
	}
	f2, err := files.Create(ctx, domain.File{Name: "b.txt", StorageKey: "key-b", UploadedBy: owner, EditedBy: owner})
	if err != nil {
		t.Fatalf("create b.txt: %v", err)
	}
	f3, err := files.Create(ctx, domain.File{Name: "c.txt", StorageKey: "key-c", UploadedBy: owner, EditedBy: owner})
	if err != nil {
		t.Fatalf("create c.txt: %v", err)
	}

	actions, newVersion, err := svc.Diff(ctx, owner, 0)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(actions) != 3 {
		t.Fatalf("actions = %+v, want exactly 3 (all three creates, not just the latest)", actions)
	}
	byName := actionsByName(actions)
	for _, want := range []struct {
		name    string
		version int64
	}{
		{"a.txt", f1.Version},
		{"b.txt", f2.Version},
		{"c.txt", f3.Version},
	} {
		a, ok := byName[want.name]
		if !ok || a.Action != ActionDownload || a.RemoteVersion != want.version {
			t.Fatalf("action for %s = %+v (ok=%v), want download at version %d", want.name, a, ok, want.version)
		}
	}
	if newVersion != f3.Version {
		t.Fatalf("newVersion = %d, want %d (the highest version among the three)", newVersion, f3.Version)
	}
}

// 4. A file created then deleted, diffed from a sinceVersion that predates
// the create.
//
// Reasoning through ListChangedSince: the file no longer exists in `files`,
// so it can never appear among `changed`. But its tombstone in
// `file_deletions` DOES have version > sinceVersion, so it WILL appear as a
// "delete_local" action -- for a file the client never downloaded in the
// first place.
//
// FINDING: this is real, current behavior (also pinned by the smoke test in
// sync_smoke_test.go's "fresh client" section) -- not a bug we're working
// around. A client that never had the file will receive a delete_local for
// it, and can only ever no-op removing a file that was never there. It's
// harmless for a well-behaved client (a local "rm" of a nonexistent path is
// a no-op), but it IS a real observable quirk of deriving deletes purely
// from a version-stamped tombstone log without cross-referencing what the
// client actually has -- worth asserting explicitly here so a future change
// to ListChangedSince (e.g. trying to "optimize" by suppressing such
// deletions) doesn't silently flip this without a test noticing.
func TestSyncServiceDiff_DeleteLocalForNeverDownloadedFile(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const owner = int64(1)

	// Client's cursor is captured BEFORE the file ever existed.
	sinceVersion := int64(0)

	f, err := files.Create(ctx, domain.File{Name: "ephemeral.txt", StorageKey: "key-e", UploadedBy: owner, EditedBy: owner})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := files.Delete(ctx, owner, f.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	actions, newVersion, err := svc.Diff(ctx, owner, sinceVersion)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions = %+v, want exactly 1 (the delete_local tombstone)", actions)
	}
	got := actions[0]
	if got.Name != "ephemeral.txt" || got.Action != ActionDeleteLocal {
		t.Fatalf("action = %+v, want {ephemeral.txt delete_local ...}", got)
	}
	// The tombstone's version is the SECOND draw from the shared counter
	// (create took the first), so it must be strictly greater than the
	// file's own (now-irrelevant) create version.
	if got.RemoteVersion <= f.Version {
		t.Fatalf("delete_local RemoteVersion = %d, want > create version %d", got.RemoteVersion, f.Version)
	}
	if newVersion != got.RemoteVersion {
		t.Fatalf("newVersion = %d, want %d (the tombstone's version)", newVersion, got.RemoteVersion)
	}
}

// 5. sinceVersion already at or beyond the current max -> empty actions,
// newVersion equals sinceVersion unchanged (not reset to e.g. the last real
// version or 0).
func TestSyncServiceDiff_AlreadyUpToDate(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const owner = int64(1)
	f, err := files.Create(ctx, domain.File{Name: "a.txt", StorageKey: "key-a", UploadedBy: owner, EditedBy: owner})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Exactly at the max version.
	actions, newVersion, err := svc.Diff(ctx, owner, f.Version)
	if err != nil {
		t.Fatalf("Diff at max: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("actions at max = %+v, want empty", actions)
	}
	if newVersion != f.Version {
		t.Fatalf("newVersion at max = %d, want unchanged %d", newVersion, f.Version)
	}

	// Beyond the max version (e.g. a stale/bogus cursor from a client).
	beyond := f.Version + 100
	actions, newVersion, err = svc.Diff(ctx, owner, beyond)
	if err != nil {
		t.Fatalf("Diff beyond max: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("actions beyond max = %+v, want empty", actions)
	}
	if newVersion != beyond {
		t.Fatalf("newVersion beyond max = %d, want unchanged %d (not reset)", newVersion, beyond)
	}
}

// 6. Diff is scoped per-user: a file belonging to a different uploadedBy
// must never appear in another user's Diff results, even though the version
// counter is shared/global across all users -- matching real Postgres,
// where files_version_seq is one global sequence but ListChangedSince
// filters by uploaded_by.
func TestSyncServiceDiff_ScopedPerUser(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const userA, userB = int64(1), int64(2)

	// Interleave creates across both users to prove the shared counter
	// doesn't leak versions/rows across the user boundary.
	fA1, err := files.Create(ctx, domain.File{Name: "a1.txt", StorageKey: "key-a1", UploadedBy: userA, EditedBy: userA})
	if err != nil {
		t.Fatalf("create a1: %v", err)
	}
	fB1, err := files.Create(ctx, domain.File{Name: "b1.txt", StorageKey: "key-b1", UploadedBy: userB, EditedBy: userB})
	if err != nil {
		t.Fatalf("create b1: %v", err)
	}
	fA2, err := files.Create(ctx, domain.File{Name: "a2.txt", StorageKey: "key-a2", UploadedBy: userA, EditedBy: userA})
	if err != nil {
		t.Fatalf("create a2: %v", err)
	}
	if err := files.Delete(ctx, userB, fB1.ID); err != nil {
		t.Fatalf("delete b1: %v", err)
	}

	// The counter is global: userA's second file must have a version well
	// past userB's, proving they share one sequence.
	if fA2.Version <= fB1.Version {
		t.Fatalf("expected shared counter to advance across users: fA2.Version=%d, fB1.Version=%d", fA2.Version, fB1.Version)
	}

	actionsA, newVersionA, err := svc.Diff(ctx, userA, 0)
	if err != nil {
		t.Fatalf("Diff userA: %v", err)
	}
	if len(actionsA) != 2 {
		t.Fatalf("userA actions = %+v, want exactly 2 (a1, a2) -- userB's b1 must not leak in", actionsA)
	}
	byNameA := actionsByName(actionsA)
	if a, ok := byNameA["a1.txt"]; !ok || a.Action != ActionDownload || a.RemoteVersion != fA1.Version {
		t.Fatalf("a1.txt action = %+v (ok=%v), want download at %d", a, ok, fA1.Version)
	}
	if a, ok := byNameA["a2.txt"]; !ok || a.Action != ActionDownload || a.RemoteVersion != fA2.Version {
		t.Fatalf("a2.txt action = %+v (ok=%v), want download at %d", a, ok, fA2.Version)
	}
	if _, leaked := byNameA["b1.txt"]; leaked {
		t.Fatalf("userA actions leaked userB's b1.txt: %+v", actionsA)
	}
	if newVersionA != fA2.Version {
		t.Fatalf("userA newVersion = %d, want %d (max among userA's own changes only)", newVersionA, fA2.Version)
	}

	actionsB, _, err := svc.Diff(ctx, userB, 0)
	if err != nil {
		t.Fatalf("Diff userB: %v", err)
	}
	if len(actionsB) != 1 || actionsB[0].Name != "b1.txt" || actionsB[0].Action != ActionDeleteLocal {
		t.Fatalf("userB actions = %+v, want exactly one delete_local for b1.txt", actionsB)
	}
}

// 7. A file changed server-side (present in `changed`) whose name the
// client's manifest also lists, with a DIFFERENT hash than the server's
// current one, is a genuine conflict: both sides changed since the client
// last synced. Diff must report "conflict", not "download", and carry the
// server's current size/modifiedAt/editedBy for the resolution UI.
func TestSyncServiceDiff_ConflictWhenHashesDiffer(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const owner = int64(1)
	modTime := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	f, err := files.Create(ctx, domain.File{
		Name: "budget.xlsx", StorageKey: "key-budget", UploadedBy: owner, EditedBy: owner,
		Size: 84_000, ModifiedAt: modTime, SHA256: "remote-hash",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	actions, newVersion, err := svc.Diff(ctx, owner, 0, ManifestEntry{Name: "budget.xlsx", SHA256: "local-hash"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions = %+v, want exactly 1", actions)
	}
	got := actions[0]
	if got.Action != ActionConflict {
		t.Fatalf("action = %+v, want Action=%q", got, ActionConflict)
	}
	if got.RemoteVersion != f.Version || got.RemoteSize != f.Size || !got.RemoteModifiedAt.Equal(f.ModifiedAt) || got.RemoteEditedBy != f.EditedBy {
		t.Fatalf("conflict details = %+v, want to mirror the server file %+v", got, f)
	}
	if newVersion != f.Version {
		t.Fatalf("newVersion = %d, want %d", newVersion, f.Version)
	}
}

// 8. A file changed server-side whose manifest hash MATCHES the server's
// current hash means the client already has the right content (e.g. it
// made the exact same edit, or already caught up via another path) -- not a
// conflict, and not worth a "download" either, so it must be silently
// dropped from the response entirely.
func TestSyncServiceDiff_NoActionWhenHashesMatch(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const owner = int64(1)
	if _, err := files.Create(ctx, domain.File{
		Name: "notes.md", StorageKey: "key-notes", UploadedBy: owner, EditedBy: owner,
		SHA256: "same-hash",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	actions, _, err := svc.Diff(ctx, owner, 0, ManifestEntry{Name: "notes.md", SHA256: "same-hash"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("actions = %+v, want empty (already in sync)", actions)
	}
}

// 9. A changed file with no hash on either side (server row predates
// migrations/0004_file_sha256, or the manifest entry's hash is empty) can't
// be compared at all -- Diff must fall back to plain "download" rather than
// guessing at a conflict it has no evidence for.
func TestSyncServiceDiff_DownloadWhenHashUnavailable(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const owner = int64(1)
	// No SHA256 set -- simulates a pre-migration row.
	f, err := files.Create(ctx, domain.File{Name: "legacy.txt", StorageKey: "key-legacy", UploadedBy: owner, EditedBy: owner})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	actions, _, err := svc.Diff(ctx, owner, 0, ManifestEntry{Name: "legacy.txt", SHA256: "whatever-the-client-has"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(actions) != 1 || actions[0].Action != ActionDownload || actions[0].RemoteVersion != f.Version {
		t.Fatalf("actions = %+v, want a single download action for legacy.txt", actions)
	}
}

// 10. A changed file whose name simply isn't in the manifest at all (the
// client never had it, or omitted the manifest entirely) is an ordinary
// download -- exactly the pre-manifest behavior, now proven to still hold
// once a (non-matching) manifest is involved elsewhere in the same call.
func TestSyncServiceDiff_DownloadWhenNameNotInManifest(t *testing.T) {
	ctx := context.Background()
	files := newFakeSyncFileRepo()
	svc := NewSyncService(files)

	const owner = int64(1)
	f, err := files.Create(ctx, domain.File{Name: "new-to-you.txt", StorageKey: "key-new", UploadedBy: owner, EditedBy: owner, SHA256: "some-hash"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Manifest mentions an unrelated file only.
	actions, _, err := svc.Diff(ctx, owner, 0, ManifestEntry{Name: "unrelated.txt", SHA256: "irrelevant"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(actions) != 1 || actions[0].Action != ActionDownload || actions[0].RemoteVersion != f.Version {
		t.Fatalf("actions = %+v, want a single download action for new-to-you.txt", actions)
	}
}
