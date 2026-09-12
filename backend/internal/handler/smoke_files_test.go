//go:build smoke

package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/authctx"
	"filespace/backend/internal/db"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/service"
	"filespace/backend/internal/storage"
)

// fakeAuth injects a fixed user id into the request context, standing in for
// the real JWTAuth middleware the Integration stage mounts in front of
// these routes in production.
func fakeAuth(userID int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(authctx.WithUserID(r.Context(), userID)))
		})
	}
}

func multipartUpload(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// TestSmokeFileHTTPRoundTrip drives the real file HTTP handlers end to end
// (upload, list w/ extension filter, get, content 415-for-.cs and 200-for-
// .java, download, delete) against a live Postgres instance
// (SMOKE_DATABASE_URL) and a real LocalFileStorage on a temp dir. Excluded
// from normal `go test ./...` via the "smoke" build tag.
func TestSmokeFileHTTPRoundTrip(t *testing.T) {
	dsn := os.Getenv("SMOKE_DATABASE_URL")
	if dsn == "" {
		t.Skip("SMOKE_DATABASE_URL not set")
	}

	sqlxDB, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sqlxDB.Close()

	store, err := storage.NewLocalFileStorage(filepath.Join(t.TempDir(), "storage"))
	if err != nil {
		t.Fatalf("new local storage: %v", err)
	}

	users := repo.NewPostgresUserRepo(sqlxDB)
	owner, err := users.Create(t.Context(), "smoke_files_http_owner", "hash")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := users.Create(t.Context(), "smoke_files_http_other", "hash")
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}

	svc := service.NewFileService(repo.NewPostgresFileRepo(sqlxDB), store)
	fh := NewFileHandler(svc)

	r := chi.NewRouter()
	r.With(fakeAuth(owner.ID)).Route("/api/files", fh.Routes)
	r.With(fakeAuth(other.ID)).Route("/api/other/files", fh.Routes)
	r.Route("/api/anon/files", fh.Routes) // no auth middleware -> should 401

	// --- unauthenticated -> 401 ---
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/anon/files", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("list without auth: status = %d, want 401", rec.Code)
	}

	// --- upload a .cs file ---
	csContent := []byte("class Program {}\n")
	body, contentType := multipartUpload(t, "Program.cs", csContent)
	req := httptest.NewRequest(http.MethodPost, "/api/files", body)
	req.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload .cs: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var csResp fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &csResp); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if csResp.Extension != "cs" || csResp.Size != int64(len(csContent)) || csResp.UploadedBy != owner.ID || csResp.EditedBy != owner.ID {
		t.Fatalf("unexpected upload response: %+v", csResp)
	}

	// --- upload a .java file ---
	javaContent := []byte("class Main {}\n")
	body, contentType = multipartUpload(t, "Main.java", javaContent)
	req = httptest.NewRequest(http.MethodPost, "/api/files", body)
	req.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload .java: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var javaResp fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &javaResp); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}

	// --- upload with no "file" field -> 400 ---
	var emptyBuf bytes.Buffer
	mw := multipart.NewWriter(&emptyBuf)
	_ = mw.Close()
	req = httptest.NewRequest(http.MethodPost, "/api/files", &emptyBuf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("upload missing file field: status = %d, want 400", rec.Code)
	}

	// --- list, filtered to extension=cs ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files?extension=cs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list extension=cs: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var listResp []fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp) != 1 || listResp[0].ID != csResp.ID {
		t.Fatalf("list extension=cs: got %+v", listResp)
	}

	// --- list from a different authenticated user sees nothing (no cross-user leakage) ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/other/files", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list as other user: status = %d", rec.Code)
	}
	var otherList []fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &otherList); err != nil {
		t.Fatalf("decode other list response: %v", err)
	}
	if len(otherList) != 0 {
		t.Fatalf("list as other user: got %+v, want empty", otherList)
	}

	// --- get by id ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/"+itoa(csResp.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get .cs: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// --- get by id as the wrong user -> 404 ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/other/files/"+itoa(csResp.ID), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get .cs as other user: status = %d, want 404", rec.Code)
	}

	// --- content: .cs -> 415 ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/"+itoa(csResp.ID)+"/content", nil))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content .cs: status = %d, want 415, body = %s", rec.Code, rec.Body.String())
	}

	// --- content: .java -> 200, text/plain, correct bytes ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/"+itoa(javaResp.ID)+"/content", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("content .java: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("content .java: Content-Type = %q", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), javaContent) {
		t.Fatalf("content .java: body mismatch, got %q", rec.Body.String())
	}

	// --- download: works for .cs (unsupported for preview but fine for download) ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/"+itoa(csResp.ID)+"/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download .cs: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Fatalf("download .cs: missing Content-Disposition")
	}
	if !bytes.Equal(rec.Body.Bytes(), csContent) {
		t.Fatalf("download .cs: body mismatch, got %q", rec.Body.String())
	}

	// --- delete as the wrong user -> 404, file still there ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/other/files/"+itoa(csResp.ID), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete .cs as other user: status = %d, want 404", rec.Code)
	}

	// --- delete as the real owner -> 204 ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/files/"+itoa(csResp.ID), nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete .cs: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// --- get after delete -> 404 ---
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/"+itoa(csResp.ID), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get .cs after delete: status = %d, want 404", rec.Code)
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
