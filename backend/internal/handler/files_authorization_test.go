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

// This file runs under plain `go test ./...` (no build tag, no live
// Postgres). It exercises two things not covered by
// files_filename_test.go:
//
//  1. Cross-user isolation: two distinct authenticated users sharing one
//     FileHandler (and thus one FileService/FileRepo/FileStorage) must not be
//     able to see, preview, download, or delete each other's files, and a
//     file owned by one user must not appear in the other's listing.
//  2. Filenames shaped like SQL injection payloads round-trip as inert bytes
//     through upload, JSON encoding, and download — proving nothing on the
//     Go side (filepath.Base/Ext, JSON, multipart decode) mishandles them.

// twoUserRouters builds ONE shared fakeFileRepo/storage/FileService/
// FileHandler and returns two chi routers over that same handler, differing
// only in which fixed user id their auth-injecting middleware sets: one
// acting as aliceID, one as bobID. Unlike newFilenameTestRouter (which
// constructs a fresh fakeFileRepo per call and is meant for single-user
// cases), these two routers deliberately share all backing state so cross-
// user visibility can be tested.
func twoUserRouters(t *testing.T, aliceID int64, aliceName string, bobID int64, bobName string) (alice, bob *chi.Mux) {
	t.Helper()

	fileRepo := newFakeFileRepo()
	userRepo := &fakeUserRepo{usernames: map[int64]string{
		aliceID: aliceName,
		bobID:   bobName,
	}}
	store, err := storage.NewLocalFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	fileSvc := service.NewFileService(fileRepo, store)
	fileHandler := NewFileHandler(fileSvc, userRepo)

	routerAs := func(userID int64) *chi.Mux {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), userID)))
			})
		})
		r.Route("/api/files", fileHandler.Routes)
		return r
	}

	return routerAs(aliceID), routerAs(bobID)
}

// TestFileAccess_IsolatedBetweenUsers proves that a file uploaded by one
// authenticated user is invisible to another, across every read/write route
// FileHandler exposes: get, content (preview), download, delete, and list.
// For get/content/download/delete this must be a 404 mapped from
// repo.ErrNotFound — never a 200, and never a different status that would
// hint the file exists under another owner. For content specifically, the
// file's extension ("java") would otherwise be eligible for preview, so a
// 404 here proves ownership is checked before the preview-support check
// (rather than, say, a 415 leaking that the file exists but isn't
// previewable). Finally it confirms alice can still perform every one of
// these operations on her own file, so the isolation is actually about
// ownership scoping and not about the routes being broken outright.
func TestFileAccess_IsolatedBetweenUsers(t *testing.T) {
	const aliceID, aliceName = int64(1), "alice"
	const bobID, bobName = int64(2), "bob"

	aliceRouter, bobRouter := twoUserRouters(t, aliceID, aliceName, bobID, bobName)

	// Alice uploads a file.
	content := []byte("class Secret {}")
	uploadRec := httptest.NewRecorder()
	aliceRouter.ServeHTTP(uploadRec, multipartUploadRequest(t, "Secret.java", content))
	if uploadRec.Code != http.StatusCreated {
		t.Fatalf("alice upload: got status %d, body=%s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploaded fileResponse
	if err := json.Unmarshal(uploadRec.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	id := uploaded.ID

	type route struct {
		name   string
		method string
		path   string
	}
	routes := []route{
		{"get", http.MethodGet, fmt.Sprintf("/api/files/%d", id)},
		{"content", http.MethodGet, fmt.Sprintf("/api/files/%d/content", id)},
		{"download", http.MethodGet, fmt.Sprintf("/api/files/%d/download", id)},
		{"delete", http.MethodDelete, fmt.Sprintf("/api/files/%d", id)},
	}

	// Bob must be refused on every route, as a 404 "file not found" — the
	// same response he'd get for a nonexistent id, giving him no signal
	// that the file exists at all, let alone that it belongs to someone
	// else.
	for _, rt := range routes {
		t.Run("bob_"+rt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			bobRouter.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("bob %s %s: got status %d, body=%s", rt.method, rt.path, rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body["error"] != "file not found" {
				t.Fatalf("bob %s %s: error = %v, want %q", rt.method, rt.path, body["error"], "file not found")
			}
		})
	}

	// Bob's delete attempt above must not have actually removed the file
	// (it should have failed with ownership-scoped ErrNotFound, not
	// silently succeeded against alice's record).
	getRec := httptest.NewRecorder()
	aliceRouter.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/files/%d", id), nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("alice get after bob's delete attempt: got status %d, body=%s", getRec.Code, getRec.Body.String())
	}

	// Bob's own file listing must not include alice's file.
	listRec := httptest.NewRecorder()
	bobRouter.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/files", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("bob list: got status %d, body=%s", listRec.Code, listRec.Body.String())
	}
	var bobList []fileResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &bobList); err != nil {
		t.Fatalf("decode bob list response: %v", err)
	}
	for _, f := range bobList {
		if f.ID == id {
			t.Fatalf("bob list includes alice's file: %+v", f)
		}
	}
	if len(bobList) != 0 {
		t.Fatalf("bob list = %+v, want empty", bobList)
	}

	// Now give bob a file of his own and re-check: his list should contain
	// exactly his own file, still not alice's.
	bobUploadRec := httptest.NewRecorder()
	bobRouter.ServeHTTP(bobUploadRec, multipartUploadRequest(t, "bobs-notes.txt", []byte("bob's notes")))
	if bobUploadRec.Code != http.StatusCreated {
		t.Fatalf("bob upload: got status %d, body=%s", bobUploadRec.Code, bobUploadRec.Body.String())
	}
	var bobUploaded fileResponse
	if err := json.Unmarshal(bobUploadRec.Body.Bytes(), &bobUploaded); err != nil {
		t.Fatalf("decode bob upload response: %v", err)
	}

	listRec2 := httptest.NewRecorder()
	bobRouter.ServeHTTP(listRec2, httptest.NewRequest(http.MethodGet, "/api/files", nil))
	if listRec2.Code != http.StatusOK {
		t.Fatalf("bob list (after his own upload): got status %d, body=%s", listRec2.Code, listRec2.Body.String())
	}
	var bobList2 []fileResponse
	if err := json.Unmarshal(listRec2.Body.Bytes(), &bobList2); err != nil {
		t.Fatalf("decode bob list response: %v", err)
	}
	if len(bobList2) != 1 || bobList2[0].ID != bobUploaded.ID {
		t.Fatalf("bob list = %+v, want exactly his own file (id=%d)", bobList2, bobUploaded.ID)
	}

	// Alice can still do everything on her own file: get, content preview
	// (java is a supported preview extension), download, and finally
	// delete.
	aliceGetRec := httptest.NewRecorder()
	aliceRouter.ServeHTTP(aliceGetRec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/files/%d", id), nil))
	if aliceGetRec.Code != http.StatusOK {
		t.Fatalf("alice get own file: got status %d, body=%s", aliceGetRec.Code, aliceGetRec.Body.String())
	}

	aliceContentRec := httptest.NewRecorder()
	aliceRouter.ServeHTTP(aliceContentRec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/files/%d/content", id), nil))
	if aliceContentRec.Code != http.StatusOK {
		t.Fatalf("alice content own file: got status %d, body=%s", aliceContentRec.Code, aliceContentRec.Body.String())
	}
	if !bytes.Equal(aliceContentRec.Body.Bytes(), content) {
		t.Fatalf("alice content own file: body = %q, want %q", aliceContentRec.Body.Bytes(), content)
	}

	aliceDownloadRec := httptest.NewRecorder()
	aliceRouter.ServeHTTP(aliceDownloadRec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/files/%d/download", id), nil))
	if aliceDownloadRec.Code != http.StatusOK {
		t.Fatalf("alice download own file: got status %d, body=%s", aliceDownloadRec.Code, aliceDownloadRec.Body.String())
	}
	if !bytes.Equal(aliceDownloadRec.Body.Bytes(), content) {
		t.Fatalf("alice download own file: body = %q, want %q", aliceDownloadRec.Body.Bytes(), content)
	}

	aliceDeleteRec := httptest.NewRecorder()
	aliceRouter.ServeHTTP(aliceDeleteRec, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/files/%d", id), nil))
	if aliceDeleteRec.Code != http.StatusNoContent {
		t.Fatalf("alice delete own file: got status %d, body=%s", aliceDeleteRec.Code, aliceDeleteRec.Body.String())
	}
}

// TestUpload_SQLInjectionShapedFilenames uploads files whose names look like
// SQL injection payloads and asserts the bytes survive untouched through
// upload -> JSON response -> download (both body and Content-Disposition
// header). This is a Go-side test: it doesn't touch a real database (that
// requires the live-Postgres smoke tier, out of scope here), but it does
// pin down that nothing in filepath.Base/Ext, the multipart decoder, or JSON
// encoding chokes on, truncates, or otherwise mangles these characters —
// necessary (though not sufficient on its own) for the real guarantee, which
// is that parameterized queries treat them as inert data rather than SQL.
func TestUpload_SQLInjectionShapedFilenames(t *testing.T) {
	const userID = 1
	const username = "mallory"

	cases := []struct {
		name      string
		filename  string
		extension string
		content   []byte
	}{
		{
			name:      "drop table txt",
			filename:  "'; DROP TABLE files; --.txt",
			extension: "txt",
			content:   []byte("payload one"),
		},
		{
			name:      "drop table png",
			filename:  "Robert'); DROP TABLE files;--.png",
			extension: "png",
			content:   []byte("payload two"),
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
