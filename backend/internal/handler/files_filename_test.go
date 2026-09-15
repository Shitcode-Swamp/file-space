package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/authctx"
	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/service"
	"filespace/backend/internal/storage"
)

// This file runs under plain `go test ./...` (no build tag, no live
// Postgres) — it fakes FileRepo/UserRepo and uses a real LocalFileStorage
// backed by a temp dir, so it exercises the actual upload -> list ->
// download HTTP path (filepath.Base/Ext filename parsing, multipart
// decoding, the JSON response, and the Content-Disposition download
// header) without needing a database.

// fakeFileRepo is an in-memory repo.FileRepo.
type fakeFileRepo struct {
	mu      sync.Mutex
	byID    map[int64]domain.File
	nextID  int64
	nextVer int64
}

func newFakeFileRepo() *fakeFileRepo {
	return &fakeFileRepo{byID: make(map[int64]domain.File)}
}

var _ repo.FileRepo = (*fakeFileRepo)(nil)

// List applies extension filtering, sorting, and limit/offset pagination
// in-memory, mirroring PostgresFileRepo.List's semantics closely enough to
// exercise FileHandler's query-param parsing and X-Has-More header without
// a live Postgres. uploadedBy/editedBy sort by raw numeric id rather than
// resolved username (this fake has no username join) — real username-based
// ordering is covered by the repo package's own Postgres-backed tests.
func (r *fakeFileRepo) List(ctx context.Context, params repo.ListParams) ([]domain.File, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.File
	for _, f := range r.byID {
		if f.UploadedBy != params.UploadedBy {
			continue
		}
		if params.Extension != "" && f.Extension != params.Extension {
			continue
		}
		out = append(out, f)
	}

	desc := strings.EqualFold(params.SortOrder, "desc")
	sort.Slice(out, func(i, j int) bool {
		var cmp int
		switch params.SortField {
		case repo.SortByCreatedAt:
			cmp = out[i].CreatedAt.Compare(out[j].CreatedAt)
		case repo.SortByModifiedAt:
			cmp = out[i].ModifiedAt.Compare(out[j].ModifiedAt)
		case repo.SortByUploadedBy:
			cmp = int(out[i].UploadedBy - out[j].UploadedBy)
		case repo.SortByEditedBy:
			cmp = int(out[i].EditedBy - out[j].EditedBy)
		default:
			cmp = strings.Compare(out[i].Name, out[j].Name)
		}
		if cmp == 0 {
			return out[i].ID < out[j].ID
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})

	if params.Limit <= 0 {
		return out, false, nil
	}
	if params.Offset >= len(out) {
		return []domain.File{}, false, nil
	}
	end := params.Offset + params.Limit
	hasMore := end < len(out)
	if end > len(out) {
		end = len(out)
	}
	return out[params.Offset:end], hasMore, nil
}

func (r *fakeFileRepo) GetByID(ctx context.Context, uploadedBy, id int64) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.byID[id]
	if !ok || f.UploadedBy != uploadedBy {
		return domain.File{}, repo.ErrNotFound
	}
	return f, nil
}

func (r *fakeFileRepo) Create(ctx context.Context, f domain.File) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	r.nextVer++
	f.ID = r.nextID
	f.Version = r.nextVer
	f.CreatedAt = time.Now()
	f.ModifiedAt = time.Now()
	r.byID[f.ID] = f
	return f, nil
}

func (r *fakeFileRepo) Delete(ctx context.Context, uploadedBy, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.byID[id]
	if !ok || f.UploadedBy != uploadedBy {
		return repo.ErrNotFound
	}
	delete(r.byID, id)
	return nil
}

func (r *fakeFileRepo) ListChangedSince(ctx context.Context, uploadedBy, sinceVersion int64) ([]domain.File, []domain.Deletion, int64, error) {
	return nil, nil, sinceVersion, nil
}

// fakeUserRepo is an in-memory repo.UserRepo with a fixed id->username map;
// only GetByID is exercised by FileHandler's usernameResolver.
type fakeUserRepo struct {
	usernames map[int64]string
}

var _ repo.UserRepo = (*fakeUserRepo)(nil)

func (r *fakeUserRepo) Create(ctx context.Context, username, passwordHash string) (domain.User, error) {
	return domain.User{}, errors.New("fakeUserRepo: Create not implemented")
}

func (r *fakeUserRepo) GetByUsername(ctx context.Context, username string) (domain.User, error) {
	return domain.User{}, errors.New("fakeUserRepo: GetByUsername not implemented")
}

func (r *fakeUserRepo) GetByID(ctx context.Context, id int64) (domain.User, error) {
	name, ok := r.usernames[id]
	if !ok {
		return domain.User{}, repo.ErrNotFound
	}
	return domain.User{ID: id, Username: name}, nil
}

// newFilenameTestRouter wires real handler/service code against fakes plus a
// real LocalFileStorage rooted at a temp dir, with a test-only middleware
// standing in for JWTAuth (auth itself is covered elsewhere; here the
// caller's identity is fixed and known).
func newFilenameTestRouter(t *testing.T, userID int64, username string) *chi.Mux {
	t.Helper()

	fileRepo := newFakeFileRepo()
	userRepo := &fakeUserRepo{usernames: map[int64]string{userID: username}}
	store, err := storage.NewLocalFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	fileSvc := service.NewFileService(fileRepo, store)
	fileHandler := NewFileHandler(fileSvc, userRepo)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), userID)))
		})
	})
	r.Route("/api/files", fileHandler.Routes)
	return r
}

func multipartUploadRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/files", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

// TestUpload_SupportsUkrainianAndEnglishFilenames drives real upload -> list
// -> download HTTP requests for both Ukrainian and English (and mixed)
// filenames, and checks the name survives byte-for-byte at every step:
// the JSON response, the file listing, and the download's
// Content-Disposition header — plus that the stored bytes round-trip
// exactly. Nothing in the stack (filepath.Base/Ext, Go's multipart decoder,
// LocalFileStorage's on-disk keys, JSON encoding) assumes ASCII, and this
// pins that down.
func TestUpload_SupportsUkrainianAndEnglishFilenames(t *testing.T) {
	const userID = 1
	const username = "соломія"

	cases := []struct {
		name      string
		filename  string
		extension string
		content   []byte
	}{
		{
			name:      "english",
			filename:  "report.txt",
			extension: "txt",
			content:   []byte("hello world"),
		},
		{
			name:      "ukrainian",
			filename:  "Звіт_про_роботу.txt",
			extension: "txt",
			content:   []byte("Привіт, світ!"),
		},
		{
			name:      "ukrainian with spaces and punctuation",
			filename:  "Договір про співпрацю №3.pdf",
			extension: "pdf",
			content:   []byte("%PDF-fake-content"),
		},
		{
			name:      "mixed ukrainian and english",
			filename:  "Report_Звіт_2026.java",
			extension: "java",
			content:   []byte("class A {}"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFilenameTestRouter(t, userID, username)

			uploadRec := httptest.NewRecorder()
			r.ServeHTTP(uploadRec, multipartUploadRequest(t, tc.filename, tc.content))
			if uploadRec.Code != http.StatusCreated {
				t.Fatalf("upload: got status %d, body=%s", uploadRec.Code, uploadRec.Body.String())
			}

			var uploaded fileResponse
			if err := json.Unmarshal(uploadRec.Body.Bytes(), &uploaded); err != nil {
				t.Fatalf("decode upload response: %v", err)
			}
			if uploaded.Name != tc.filename {
				t.Fatalf("uploaded Name = %q, want %q", uploaded.Name, tc.filename)
			}
			if uploaded.Extension != tc.extension {
				t.Fatalf("uploaded Extension = %q, want %q", uploaded.Extension, tc.extension)
			}
			if uploaded.UploadedBy != username || uploaded.EditedBy != username {
				t.Fatalf("uploaded UploadedBy/EditedBy = %q/%q, want %q", uploaded.UploadedBy, uploaded.EditedBy, username)
			}

			listRec := httptest.NewRecorder()
			r.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/files", nil))
			if listRec.Code != http.StatusOK {
				t.Fatalf("list: got status %d, body=%s", listRec.Code, listRec.Body.String())
			}
			var listed []fileResponse
			if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
				t.Fatalf("decode list response: %v", err)
			}
			if len(listed) != 1 || listed[0].Name != tc.filename {
				t.Fatalf("list = %+v, want a single entry named %q", listed, tc.filename)
			}

			downloadRec := httptest.NewRecorder()
			downloadPath := fmt.Sprintf("/api/files/%d/download", uploaded.ID)
			r.ServeHTTP(downloadRec, httptest.NewRequest(http.MethodGet, downloadPath, nil))
			if downloadRec.Code != http.StatusOK {
				t.Fatalf("download: got status %d, body=%s", downloadRec.Code, downloadRec.Body.String())
			}
			if !bytes.Equal(downloadRec.Body.Bytes(), tc.content) {
				t.Fatalf("downloaded content = %q, want %q", downloadRec.Body.Bytes(), tc.content)
			}
			wantDisposition := contentDispositionAttachment(tc.filename)
			if got := downloadRec.Header().Get("Content-Disposition"); got != wantDisposition {
				t.Fatalf("Content-Disposition = %q, want %q", got, wantDisposition)
			}
		})
	}
}
