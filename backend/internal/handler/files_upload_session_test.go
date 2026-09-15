package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/authctx"
	"filespace/backend/internal/service"
	"filespace/backend/internal/storage"
)

// This file drives the chunked-upload session routes (POST .../uploads,
// PUT .../uploads/{id}/chunks/{index}, POST .../uploads/{id}/complete,
// DELETE .../uploads/{id}) as real HTTP requests against newFilenameTestRouter
// (see files_filename_test.go), the same fake-repo/real-LocalFileStorage
// router the filename round-trip tests use.

func newInitiateUploadHTTPRequest(t *testing.T, filename string, size int64) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"filename": filename, "size": size})
	if err != nil {
		t.Fatalf("marshal initiate body: %v", err)
	}
	return httptest.NewRequest(http.MethodPost, "/api/files/uploads", bytes.NewReader(body))
}

func chunkRequest(uploadID string, index int, chunk []byte) *http.Request {
	path := fmt.Sprintf("/api/files/uploads/%s/chunks/%d", uploadID, index)
	return httptest.NewRequest(http.MethodPut, path, bytes.NewReader(chunk))
}

func completeRequest(uploadID string) *http.Request {
	return httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/files/uploads/%s/complete", uploadID), nil)
}

// doInitiate runs the initiate request and decodes its response.
func doInitiate(t *testing.T, r http.Handler, filename string, size int64) initiateUploadResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, newInitiateUploadHTTPRequest(t, filename, size))
	if rec.Code != http.StatusCreated {
		t.Fatalf("initiate: got status %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp initiateUploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode initiate response: %v", err)
	}
	if resp.UploadID == "" {
		t.Fatalf("initiate response has empty uploadId: %+v", resp)
	}
	return resp
}

func TestUploadSession_HappyPathMatchesSingleShotResponseShape(t *testing.T) {
	r := newFilenameTestRouter(t, 1, "someone")
	content := []byte("hello, chunked world!")

	init := doInitiate(t, r, "notes.txt", int64(len(content)))

	// Two chunks, arbitrarily split.
	chunks := [][]byte{content[:10], content[10:]}
	for i, chunk := range chunks {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, chunkRequest(init.UploadID, i, chunk))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("chunk %d: got status %d, body=%s", i, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, completeRequest(init.UploadID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete: got status %d, body=%s", rec.Code, rec.Body.String())
	}
	var fr fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &fr); err != nil {
		t.Fatalf("decode complete response: %v", err)
	}
	if fr.Name != "notes.txt" || fr.Size != int64(len(content)) {
		t.Fatalf("complete response = %+v, want name=notes.txt size=%d", fr, len(content))
	}

	// The finished file shows up in the normal listing, just like a
	// single-shot upload would.
	listRec := httptest.NewRecorder()
	r.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/files", nil))
	var listed []fileResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != fr.ID {
		t.Fatalf("list = %+v, want the single completed upload", listed)
	}

	downloadRec := httptest.NewRecorder()
	r.ServeHTTP(downloadRec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/files/%d/download", fr.ID), nil))
	if downloadRec.Code != http.StatusOK || !bytes.Equal(downloadRec.Body.Bytes(), content) {
		t.Fatalf("download: status=%d body=%q, want 200 and %q", downloadRec.Code, downloadRec.Body.String(), content)
	}
}

func TestUploadSession_OutOfOrderChunkRejected(t *testing.T) {
	r := newFilenameTestRouter(t, 1, "someone")
	init := doInitiate(t, r, "f.txt", 5)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, chunkRequest(init.UploadID, 1, []byte("x")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("chunk out of order: got status %d, body=%s, want %d", rec.Code, rec.Body.String(), http.StatusConflict)
	}
}

func TestUploadSession_UnknownIDIs404ForEveryOperation(t *testing.T) {
	r := newFilenameTestRouter(t, 1, "someone")

	cases := []*http.Request{
		chunkRequest("no-such-id", 0, []byte("x")),
		completeRequest("no-such-id"),
		httptest.NewRequest(http.MethodDelete, "/api/files/uploads/no-such-id", nil),
	}
	for _, req := range cases {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: got status %d, body=%s, want 404", req.Method, req.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

func TestUploadSession_SizeMismatchOnComplete(t *testing.T) {
	r := newFilenameTestRouter(t, 1, "someone")
	init := doInitiate(t, r, "f.txt", 100)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, chunkRequest(init.UploadID, 0, []byte("too short")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("chunk: got status %d, body=%s", rec.Code, rec.Body.String())
	}

	completeRec := httptest.NewRecorder()
	r.ServeHTTP(completeRec, completeRequest(init.UploadID))
	if completeRec.Code != http.StatusBadRequest {
		t.Fatalf("complete: got status %d, body=%s, want 400", completeRec.Code, completeRec.Body.String())
	}
}

func TestUploadSession_AbortThenChunkIs404(t *testing.T) {
	r := newFilenameTestRouter(t, 1, "someone")
	init := doInitiate(t, r, "f.txt", 5)

	abortRec := httptest.NewRecorder()
	r.ServeHTTP(abortRec, httptest.NewRequest(http.MethodDelete, "/api/files/uploads/"+init.UploadID, nil))
	if abortRec.Code != http.StatusNoContent {
		t.Fatalf("abort: got status %d, body=%s", abortRec.Code, abortRec.Body.String())
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, chunkRequest(init.UploadID, 0, []byte("hello")))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("chunk after abort: got status %d, want 404", rec.Code)
	}
}

// twoUserRoutersSharingOneService builds two routers backed by the *same*
// FileService (and so the same in-memory upload session manager) — the only
// way to exercise "does another authenticated user see my in-progress
// upload" at the HTTP layer, since two independent newFilenameTestRouter
// calls each get their own, unrelated session manager.
func twoUserRoutersSharingOneService(t *testing.T, ownerID int64, ownerName string, otherID int64, otherName string) (owner, other *chi.Mux) {
	t.Helper()
	fileRepo := newFakeFileRepo()
	userRepo := &fakeUserRepo{usernames: map[int64]string{ownerID: ownerName, otherID: otherName}}
	store, err := storage.NewLocalFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	fileSvc := service.NewFileService(fileRepo, store)
	fileHandler := NewFileHandler(fileSvc, userRepo)

	build := func(userID int64) *chi.Mux {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), userID)))
			})
		})
		r.Route("/api/files", fileHandler.Routes)
		return r
	}
	return build(ownerID), build(otherID)
}

func TestUploadSession_ScopedToOwner(t *testing.T) {
	ownerRouter, otherRouter := twoUserRoutersSharingOneService(t, 1, "owner", 2, "other")
	init := doInitiate(t, ownerRouter, "f.txt", 5)

	rec := httptest.NewRecorder()
	otherRouter.ServeHTTP(rec, chunkRequest(init.UploadID, 0, []byte("hello")))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("chunk as other user: got status %d, want 404", rec.Code)
	}

	// The real owner can still use it -- the other user's attempt didn't
	// corrupt or consume the session.
	ownerRec := httptest.NewRecorder()
	ownerRouter.ServeHTTP(ownerRec, chunkRequest(init.UploadID, 0, []byte("hello")))
	if ownerRec.Code != http.StatusNoContent {
		t.Fatalf("chunk as owner: got status %d, body=%s", ownerRec.Code, ownerRec.Body.String())
	}
}
