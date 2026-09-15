package service

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/storage"
)

// fakeUploadsFileRepo is a minimal in-memory repo.FileRepo — just enough for
// FileService.Create (called at the end of CompleteUpload) to work, without
// needing a live Postgres. Chunking/session logic lives entirely in
// FileService/uploadSessionManager, independent of which FileRepo backs it.
type fakeUploadsFileRepo struct {
	mu     sync.Mutex
	byID   map[int64]domain.File
	nextID int64
}

func newFakeUploadsFileRepo() *fakeUploadsFileRepo {
	return &fakeUploadsFileRepo{byID: make(map[int64]domain.File)}
}

var _ repo.FileRepo = (*fakeUploadsFileRepo)(nil)

func (r *fakeUploadsFileRepo) List(ctx context.Context, params repo.ListParams) ([]domain.File, bool, error) {
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

func (r *fakeUploadsFileRepo) GetByID(ctx context.Context, uploadedBy, id int64) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.byID[id]
	if !ok || f.UploadedBy != uploadedBy {
		return domain.File{}, repo.ErrNotFound
	}
	return f, nil
}

func (r *fakeUploadsFileRepo) Create(ctx context.Context, f domain.File) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	f.ID = r.nextID
	r.byID[f.ID] = f
	return f, nil
}

func (r *fakeUploadsFileRepo) Delete(ctx context.Context, uploadedBy, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.byID[id]; !ok || f.UploadedBy != uploadedBy {
		return repo.ErrNotFound
	}
	delete(r.byID, id)
	return nil
}

func (r *fakeUploadsFileRepo) ListChangedSince(ctx context.Context, uploadedBy, sinceVersion int64) ([]domain.File, []domain.Deletion, int64, error) {
	return nil, nil, sinceVersion, nil
}

func newUploadsTestService(t *testing.T) *FileService {
	t.Helper()
	store, err := storage.NewLocalFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	return NewFileService(newFakeUploadsFileRepo(), store)
}

// splitIntoChunks splits content into n roughly-equal pieces, in order.
func splitIntoChunks(content []byte, n int) [][]byte {
	if n <= 0 {
		return nil
	}
	size := (len(content) + n - 1) / n
	if size == 0 {
		size = 1
	}
	var chunks [][]byte
	for i := 0; i < len(content); i += size {
		end := i + size
		if end > len(content) {
			end = len(content)
		}
		chunks = append(chunks, content[i:end])
	}
	return chunks
}

func TestChunkedUpload_HappyPath(t *testing.T) {
	svc := newUploadsTestService(t)
	ctx := context.Background()
	const userID = 1
	content := bytes.Repeat([]byte("filespace-chunk-data-"), 1000) // a few KB, split into several chunks

	uploadID, chunkSize, err := svc.InitiateUpload(ctx, userID, "big.bin", int64(len(content)))
	if err != nil {
		t.Fatalf("InitiateUpload: %v", err)
	}
	if chunkSize != UploadChunkSize {
		t.Fatalf("chunkSize = %d, want %d", chunkSize, UploadChunkSize)
	}

	for i, chunk := range splitIntoChunks(content, 5) {
		if err := svc.UploadChunk(ctx, userID, uploadID, i, bytes.NewReader(chunk)); err != nil {
			t.Fatalf("UploadChunk(index=%d): %v", i, err)
		}
	}

	created, err := svc.CompleteUpload(ctx, userID, uploadID)
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if created.Name != "big.bin" || created.Size != int64(len(content)) {
		t.Fatalf("created = %+v, want name=big.bin size=%d", created, len(content))
	}

	rc, err := svc.storage.Open(ctx, created.StorageKey)
	if err != nil {
		t.Fatalf("open stored content: %v", err)
	}
	defer rc.Close()
	var gotBuf bytes.Buffer
	if _, err := gotBuf.ReadFrom(rc); err != nil {
		t.Fatalf("read stored content: %v", err)
	}
	if !bytes.Equal(gotBuf.Bytes(), content) {
		t.Fatalf("stored content does not match the reassembled chunks (len got=%d want=%d)", gotBuf.Len(), len(content))
	}

	// The session is gone after a successful complete.
	if err := svc.UploadChunk(ctx, userID, uploadID, 0, bytes.NewReader(nil)); !errors.Is(err, ErrUploadSessionNotFound) {
		t.Fatalf("UploadChunk after complete: err = %v, want ErrUploadSessionNotFound", err)
	}
}

func TestChunkedUpload_OutOfOrderChunkRejected(t *testing.T) {
	svc := newUploadsTestService(t)
	ctx := context.Background()

	uploadID, _, err := svc.InitiateUpload(ctx, 1, "f.txt", 10)
	if err != nil {
		t.Fatalf("InitiateUpload: %v", err)
	}

	if err := svc.UploadChunk(ctx, 1, uploadID, 1, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrUploadChunkOutOfOrder) {
		t.Fatalf("UploadChunk at index 1 first: err = %v, want ErrUploadChunkOutOfOrder", err)
	}

	// index 0 still works afterward -- the rejected attempt didn't advance
	// nextChunk or corrupt the session.
	if err := svc.UploadChunk(ctx, 1, uploadID, 0, bytes.NewReader([]byte("0123456789"))); err != nil {
		t.Fatalf("UploadChunk at index 0: %v", err)
	}
	if _, err := svc.CompleteUpload(ctx, 1, uploadID); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
}

func TestChunkedUpload_SizeMismatchOnComplete(t *testing.T) {
	svc := newUploadsTestService(t)
	ctx := context.Background()

	uploadID, _, err := svc.InitiateUpload(ctx, 1, "f.txt", 10)
	if err != nil {
		t.Fatalf("InitiateUpload: %v", err)
	}
	if err := svc.UploadChunk(ctx, 1, uploadID, 0, bytes.NewReader([]byte("too short"))); err != nil {
		t.Fatalf("UploadChunk: %v", err)
	}

	if _, err := svc.CompleteUpload(ctx, 1, uploadID); !errors.Is(err, ErrUploadSizeMismatch) {
		t.Fatalf("CompleteUpload: err = %v, want ErrUploadSizeMismatch", err)
	}

	// A failed complete still discards the session (no half-finished upload
	// left lying around to retry against).
	if err := svc.UploadChunk(ctx, 1, uploadID, 1, bytes.NewReader(nil)); !errors.Is(err, ErrUploadSessionNotFound) {
		t.Fatalf("UploadChunk after failed complete: err = %v, want ErrUploadSessionNotFound", err)
	}
}

func TestChunkedUpload_ScopedToOwner(t *testing.T) {
	svc := newUploadsTestService(t)
	ctx := context.Background()
	const owner, other = 1, 2

	uploadID, _, err := svc.InitiateUpload(ctx, owner, "f.txt", 5)
	if err != nil {
		t.Fatalf("InitiateUpload: %v", err)
	}

	if err := svc.UploadChunk(ctx, other, uploadID, 0, bytes.NewReader([]byte("hello"))); !errors.Is(err, ErrUploadSessionNotFound) {
		t.Fatalf("UploadChunk as other user: err = %v, want ErrUploadSessionNotFound", err)
	}
	if _, err := svc.CompleteUpload(ctx, other, uploadID); !errors.Is(err, ErrUploadSessionNotFound) {
		t.Fatalf("CompleteUpload as other user: err = %v, want ErrUploadSessionNotFound", err)
	}
	if err := svc.AbortUpload(ctx, other, uploadID); !errors.Is(err, ErrUploadSessionNotFound) {
		t.Fatalf("AbortUpload as other user: err = %v, want ErrUploadSessionNotFound", err)
	}

	// The owner can still use it -- the other user's attempts didn't corrupt
	// or consume the session.
	if err := svc.UploadChunk(ctx, owner, uploadID, 0, bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("UploadChunk as owner: %v", err)
	}
}

func TestChunkedUpload_AbortDiscardsSession(t *testing.T) {
	svc := newUploadsTestService(t)
	ctx := context.Background()

	uploadID, _, err := svc.InitiateUpload(ctx, 1, "f.txt", 5)
	if err != nil {
		t.Fatalf("InitiateUpload: %v", err)
	}
	if err := svc.AbortUpload(ctx, 1, uploadID); err != nil {
		t.Fatalf("AbortUpload: %v", err)
	}
	if err := svc.UploadChunk(ctx, 1, uploadID, 0, bytes.NewReader([]byte("hello"))); !errors.Is(err, ErrUploadSessionNotFound) {
		t.Fatalf("UploadChunk after abort: err = %v, want ErrUploadSessionNotFound", err)
	}
	if err := svc.AbortUpload(ctx, 1, uploadID); !errors.Is(err, ErrUploadSessionNotFound) {
		t.Fatalf("AbortUpload twice: err = %v, want ErrUploadSessionNotFound", err)
	}
}

func TestInitiateUpload_RejectsInvalidInput(t *testing.T) {
	svc := newUploadsTestService(t)
	ctx := context.Background()

	if _, _, err := svc.InitiateUpload(ctx, 1, "", 10); !errors.Is(err, ErrInvalidFilename) {
		t.Fatalf("empty filename: err = %v, want ErrInvalidFilename", err)
	}
	if _, _, err := svc.InitiateUpload(ctx, 1, "f.txt", maxUploadSessionSize+1); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("oversized declaration: err = %v, want ErrUploadTooLarge", err)
	}
	if _, _, err := svc.InitiateUpload(ctx, 1, "f.txt", -1); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("negative size: err = %v, want ErrUploadTooLarge", err)
	}
}
