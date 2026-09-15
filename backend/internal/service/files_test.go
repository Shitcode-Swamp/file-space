package service

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/storage"
)

// fakeNameConstrainedFileRepo is an in-memory repo.FileRepo that enforces
// the same (uploaded_by, name) uniqueness
// migrations/0003_unique_file_name_per_owner.up.sql adds in Postgres,
// returning a *pgconn.PgError shaped exactly like the real unique_violation
// so FileService.Create's isNameConflict detection is exercised faithfully
// without needing a live Postgres.
type fakeNameConstrainedFileRepo struct {
	mu     sync.Mutex
	byID   map[int64]domain.File
	nextID int64
}

func newFakeNameConstrainedFileRepo() *fakeNameConstrainedFileRepo {
	return &fakeNameConstrainedFileRepo{byID: make(map[int64]domain.File)}
}

var _ repo.FileRepo = (*fakeNameConstrainedFileRepo)(nil)

func (r *fakeNameConstrainedFileRepo) List(ctx context.Context, params repo.ListParams) ([]domain.File, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.File
	for _, f := range r.byID {
		if f.UploadedBy == params.UploadedBy {
			out = append(out, f)
		}
	}
	return out, false, nil
}

func (r *fakeNameConstrainedFileRepo) GetByID(ctx context.Context, uploadedBy, id int64) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.byID[id]
	if !ok || f.UploadedBy != uploadedBy {
		return domain.File{}, repo.ErrNotFound
	}
	return f, nil
}

func (r *fakeNameConstrainedFileRepo) Create(ctx context.Context, f domain.File) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.byID {
		if existing.UploadedBy == f.UploadedBy && existing.Name == f.Name {
			return domain.File{}, &pgconn.PgError{Code: "23505", ConstraintName: "files_uploaded_by_name_key"}
		}
	}
	r.nextID++
	f.ID = r.nextID
	r.byID[f.ID] = f
	return f, nil
}

func (r *fakeNameConstrainedFileRepo) Delete(ctx context.Context, uploadedBy, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.byID[id]; !ok || f.UploadedBy != uploadedBy {
		return repo.ErrNotFound
	}
	delete(r.byID, id)
	return nil
}

func (r *fakeNameConstrainedFileRepo) ListChangedSince(ctx context.Context, uploadedBy, sinceVersion int64) ([]domain.File, []domain.Deletion, int64, error) {
	return nil, nil, sinceVersion, nil
}

func newDedupTestService(t *testing.T) *FileService {
	t.Helper()
	store, err := storage.NewLocalFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	return NewFileService(newFakeNameConstrainedFileRepo(), store)
}

// TestCreate_RenamesOnDuplicateNameForSameUser covers the core behavior:
// uploading the same filename twice must not fail or silently overwrite --
// it renames "name (1).ext", "name (2).ext", etc. This is what keeps the
// desktop sync client's name-keyed matching (SyncViewModel.swift) from ever
// seeing two files with the same name for one user.
func TestCreate_RenamesOnDuplicateNameForSameUser(t *testing.T) {
	svc := newDedupTestService(t)
	ctx := context.Background()

	first, err := svc.Create(ctx, 1, "report.pdf", bytes.NewReader([]byte("v1")))
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if first.Name != "report.pdf" {
		t.Fatalf("first.Name = %q, want %q", first.Name, "report.pdf")
	}

	second, err := svc.Create(ctx, 1, "report.pdf", bytes.NewReader([]byte("v2")))
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if second.Name != "report (1).pdf" {
		t.Fatalf("second.Name = %q, want %q", second.Name, "report (1).pdf")
	}

	third, err := svc.Create(ctx, 1, "report.pdf", bytes.NewReader([]byte("v3")))
	if err != nil {
		t.Fatalf("create third: %v", err)
	}
	if third.Name != "report (2).pdf" {
		t.Fatalf("third.Name = %q, want %q", third.Name, "report (2).pdf")
	}

	if first.ID == second.ID || second.ID == third.ID || first.ID == third.ID {
		t.Fatalf("expected three distinct files, got ids %d, %d, %d", first.ID, second.ID, third.ID)
	}
}

func TestCreate_DedupsNamesWithoutAnExtension(t *testing.T) {
	svc := newDedupTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, 1, "README", bytes.NewReader([]byte("a"))); err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := svc.Create(ctx, 1, "README", bytes.NewReader([]byte("b")))
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if second.Name != "README (1)" {
		t.Fatalf("second.Name = %q, want %q", second.Name, "README (1)")
	}
}

// TestCreate_SameNameDifferentUsersDoesNotCollide pins that the dedup is
// scoped per-owner: the constraint is (uploaded_by, name), not name alone.
func TestCreate_SameNameDifferentUsersDoesNotCollide(t *testing.T) {
	svc := newDedupTestService(t)
	ctx := context.Background()

	a, err := svc.Create(ctx, 1, "shared.txt", bytes.NewReader([]byte("a")))
	if err != nil {
		t.Fatalf("create for user 1: %v", err)
	}
	b, err := svc.Create(ctx, 2, "shared.txt", bytes.NewReader([]byte("b")))
	if err != nil {
		t.Fatalf("create for user 2: %v", err)
	}
	if a.Name != "shared.txt" || b.Name != "shared.txt" {
		t.Fatalf("expected both users to keep the unmodified name, got %q and %q", a.Name, b.Name)
	}
}

func TestDedupedName(t *testing.T) {
	cases := []struct {
		base string
		n    int
		want string
	}{
		{"report.pdf", 1, "report (1).pdf"},
		{"archive.tar.gz", 1, "archive.tar (1).gz"},
		{"README", 2, "README (2)"},
	}
	for _, tc := range cases {
		if got := dedupedName(tc.base, tc.n); got != tc.want {
			t.Errorf("dedupedName(%q, %d) = %q, want %q", tc.base, tc.n, got, tc.want)
		}
	}
}
