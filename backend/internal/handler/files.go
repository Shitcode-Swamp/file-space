package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/authctx"
	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/service"
)

// maxUploadMemory bounds how much of a multipart upload is buffered in
// memory before spilling to temp files; it does not limit the maximum file
// size — see maxUploadSize for that.
const maxUploadMemory = 32 << 20 // 32 MiB

// maxUploadSize is the hard cap on a single upload's total request body,
// enforced via http.MaxBytesReader so an authenticated caller can't exhaust
// server disk by streaming an unbounded file.
const maxUploadSize = 512 << 20 // 512 MiB

// FileHandler exposes the /api/files/* endpoints. Every method requires an
// authenticated caller (authctx.UserID populated by the JWT middleware the
// Integration stage mounts in front of these routes); if it isn't present,
// handlers respond 401 rather than assuming an anonymous caller.
type FileHandler struct {
	svc   *service.FileService
	users repo.UserRepo
}

// NewFileHandler constructs a FileHandler backed by svc, resolving
// uploadedBy/editedBy ids to usernames via users.
func NewFileHandler(svc *service.FileService, users repo.UserRepo) *FileHandler {
	return &FileHandler{svc: svc, users: users}
}

// Routes registers the file endpoints onto r, so the Integration stage can
// mount this handler without knowing its internals, e.g.:
//
//	r.Route("/api/files", fileHandler.Routes)
func (h *FileHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.upload)
	r.Get("/{id}", h.get)
	r.Get("/{id}/content", h.content)
	r.Get("/{id}/download", h.download)
	r.Delete("/{id}", h.delete)

	// Chunked upload session, for large files and upload progress — see
	// service/uploads.go. "uploads" is a static segment, so chi matches it
	// ahead of the "/{id}" param routes above rather than colliding with them.
	r.Route("/uploads", func(ur chi.Router) {
		ur.Post("/", h.initiateUpload)
		ur.Put("/{uploadID}/chunks/{index}", h.uploadChunk)
		ur.Post("/{uploadID}/complete", h.completeUpload)
		ur.Delete("/{uploadID}", h.abortUpload)
	})
}

// fileResponse is the JSON shape returned for a single file's metadata.
// uploadedBy/editedBy are the *usernames* of the users who uploaded/last
// edited the file, resolved from the domain.File's numeric ids via
// FileHandler.users.
type fileResponse struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Extension  string    `json:"extension"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"createdAt"`
	ModifiedAt time.Time `json:"modifiedAt"`
	UploadedBy string    `json:"uploadedBy"`
	EditedBy   string    `json:"editedBy"`
}

func toFileResponse(f domain.File, uploadedBy, editedBy string) fileResponse {
	return fileResponse{
		ID:         f.ID,
		Name:       f.Name,
		Extension:  f.Extension,
		Size:       f.Size,
		CreatedAt:  f.CreatedAt,
		ModifiedAt: f.ModifiedAt,
		UploadedBy: uploadedBy,
		EditedBy:   editedBy,
	}
}

// usernameResolver resolves user ids to usernames, caching lookups so a
// given id is only fetched from h.users once per request.
type usernameResolver struct {
	users repo.UserRepo
	cache map[int64]string
}

func (h *FileHandler) newUsernameResolver() *usernameResolver {
	return &usernameResolver{users: h.users, cache: make(map[int64]string)}
}

// resolve returns the username for id, fetching it via UserRepo.GetByID and
// caching the result if it isn't already cached. A file referencing a
// nonexistent user id is a data integrity bug, so lookup failures are
// propagated as errors rather than papered over.
func (r *usernameResolver) resolve(ctx context.Context, id int64) (string, error) {
	if name, ok := r.cache[id]; ok {
		return name, nil
	}
	u, err := r.users.GetByID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("handler: resolve username for user id=%d: %w", id, err)
	}
	r.cache[id] = u.Username
	return u.Username, nil
}

// toResponse resolves f's uploadedBy/editedBy ids to usernames and builds
// its fileResponse.
func (r *usernameResolver) toResponse(ctx context.Context, f domain.File) (fileResponse, error) {
	uploadedBy, err := r.resolve(ctx, f.UploadedBy)
	if err != nil {
		return fileResponse{}, err
	}
	editedBy, err := r.resolve(ctx, f.EditedBy)
	if err != nil {
		return fileResponse{}, err
	}
	return toFileResponse(f, uploadedBy, editedBy), nil
}

// requireUserID reads the authenticated caller's id from the request
// context, writing a 401 and returning ok==false if it's missing (e.g. these
// routes were mounted without the JWT middleware in front of them).
func requireUserID(w http.ResponseWriter, r *http.Request) (userID int64, ok bool) {
	userID, ok = authctx.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
	return userID, ok
}

func parseFileID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

// listSortFields maps the "sort" query param to a repo.SortField. An
// unrecognized or missing value defaults to repo.SortByName (the zero
// value), matching the web client's default sort.
var listSortFields = map[string]repo.SortField{
	"name":       repo.SortByName,
	"createdAt":  repo.SortByCreatedAt,
	"modifiedAt": repo.SortByModifiedAt,
	"uploadedBy": repo.SortByUploadedBy,
	"editedBy":   repo.SortByEditedBy,
}

// defaultListLimit/maxListLimit bound the "limit" query param: callers that
// don't specify one get defaultListLimit files per page (the web client's
// infinite scroll), and no caller can request more than maxListLimit at
// once — the desktop client, which wants everything in a single call,
// requests exactly maxListLimit (see APIClient.swift).
const (
	defaultListLimit = 50
	maxListLimit     = 1000
)

// list handles GET /api/files?sort=name|createdAt|modifiedAt|uploadedBy|editedBy
// &order=asc|desc&extension=cs&limit=50&offset=0. The response is a bare
// JSON array of files (unchanged shape) plus an X-Has-More: true|false
// header reporting whether more files exist beyond this page.
func (h *FileHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	params := repo.ListParams{UploadedBy: userID, Limit: defaultListLimit}

	params.SortField = listSortFields[q.Get("sort")]

	if strings.ToLower(strings.TrimSpace(q.Get("order"))) == "desc" {
		params.SortOrder = "desc"
	} else {
		params.SortOrder = "asc"
	}

	if ext := strings.TrimPrefix(strings.TrimSpace(q.Get("extension")), "."); ext != "" {
		params.Extension = ext
	}

	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		params.Limit = n
	}
	if params.Limit > maxListLimit {
		params.Limit = maxListLimit
	}

	if n, err := strconv.Atoi(q.Get("offset")); err == nil && n >= 0 {
		params.Offset = n
	}

	files, hasMore, err := h.svc.List(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	resolver := h.newUsernameResolver()
	resp := make([]fileResponse, 0, len(files))
	for _, f := range files {
		fr, err := resolver.toResponse(r.Context(), f)
		if err != nil {
			log.Printf("handler: %v", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		resp = append(resp, fr)
	}
	w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))
	writeJSON(w, http.StatusOK, resp)
}

// get handles GET /api/files/{id}
func (h *FileHandler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, err := parseFileID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file id")
		return
	}

	f, err := h.svc.Get(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	fr, err := h.newUsernameResolver().toResponse(r.Context(), f)
	if err != nil {
		log.Printf("handler: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, fr)
}

// content handles GET /api/files/{id}/content — inline preview, restricted
// to the extensions REQUIREMENTS.md §2 requires structured display for.
func (h *FileHandler) content(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, err := parseFileID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file id")
		return
	}

	f, rc, err := h.svc.Content(r.Context(), userID, id)
	if err != nil {
		switch {
		case errors.Is(err, repo.ErrNotFound):
			writeError(w, http.StatusNotFound, "file not found")
		case errors.Is(err, service.ErrPreviewNotSupported):
			writeError(w, http.StatusUnsupportedMediaType, "preview not supported for this file type")
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", service.ContentType(f.Extension))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("handler: stream content for file id=%d: %v", id, err)
	}
}

// contentDispositionAttachment builds a Content-Disposition header value
// that survives non-ASCII filenames. Browsers' fetch() decodes response
// header bytes as Latin-1, so raw UTF-8 bytes in a plain filename="..."
// parameter come back mangled once read via Headers.get(). The RFC 6266
// filename* parameter percent-encodes the UTF-8 bytes (pure ASCII on the
// wire) and is what the frontend's client.ts prefers; filename="..." is
// kept as an ASCII-safe fallback for clients that ignore filename*.
func contentDispositionAttachment(name string) string {
	fallback := name
	if !isASCII(name) {
		fallback = asciiFallback(name)
	}
	encoded := strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", fallback, encoded)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func asciiFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r <= unicode.MaxASCII {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// download handles GET /api/files/{id}/download — works for every
// extension, unlike content preview.
func (h *FileHandler) download(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, err := parseFileID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file id")
		return
	}

	f, rc, err := h.svc.Download(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDispositionAttachment(f.Name))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("handler: stream download for file id=%d: %v", id, err)
	}
}

// upload handles POST /api/files — multipart/form-data with the file in a
// "file" field.
func (h *FileHandler) upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadMemory); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "file too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	if r.MultipartForm != nil {
		defer func() {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				log.Printf("handler: cleanup multipart form: %v", err)
			}
		}()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, `missing "file" field`)
		return
	}
	defer file.Close()

	created, err := h.svc.Create(r.Context(), userID, header.Filename, file)
	if err != nil {
		if errors.Is(err, service.ErrInvalidFilename) {
			writeError(w, http.StatusBadRequest, "invalid filename")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	fr, err := h.newUsernameResolver().toResponse(r.Context(), created)
	if err != nil {
		log.Printf("handler: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusCreated, fr)
}

// maxChunkBodySize bounds a single PUT .../chunks/{index} request body.
// It's generously above service.UploadChunkSize (what clients are told to
// send) so a well-behaved client is never rejected; it exists only to stop
// a misbehaving one from streaming an unbounded body into a single chunk.
const maxChunkBodySize = 2 * service.UploadChunkSize

type initiateUploadRequest struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type initiateUploadResponse struct {
	UploadID  string `json:"uploadId"`
	ChunkSize int64  `json:"chunkSize"`
}

// initiateUpload handles POST /api/files/uploads — starts a chunked-upload
// session for a file of the declared name/size, returning the session id
// and the chunk size the client must upload in.
func (h *FileHandler) initiateUpload(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req initiateUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	uploadID, chunkSize, err := h.svc.InitiateUpload(r.Context(), userID, req.Filename, req.Size)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidFilename):
			writeError(w, http.StatusBadRequest, "invalid filename")
		case errors.Is(err, service.ErrUploadTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "file too large")
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusCreated, initiateUploadResponse{UploadID: uploadID, ChunkSize: chunkSize})
}

// uploadChunk handles PUT /api/files/uploads/{uploadID}/chunks/{index} — the
// request body is the chunk's raw bytes. Chunks must arrive in order
// starting at index 0.
func (h *FileHandler) uploadChunk(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	uploadID := chi.URLParam(r, "uploadID")
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || index < 0 {
		writeError(w, http.StatusBadRequest, "invalid chunk index")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxChunkBodySize)
	if err := h.svc.UploadChunk(r.Context(), userID, uploadID, index, r.Body); err != nil {
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxBytesErr):
			writeError(w, http.StatusRequestEntityTooLarge, "chunk too large")
		case errors.Is(err, service.ErrUploadSessionNotFound):
			writeError(w, http.StatusNotFound, "upload session not found")
		case errors.Is(err, service.ErrUploadChunkOutOfOrder):
			writeError(w, http.StatusConflict, "chunk out of order")
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// completeUpload handles POST /api/files/uploads/{uploadID}/complete —
// finalizes the session into a stored file once every chunk has arrived,
// responding with the same shape as the single-shot upload endpoint.
func (h *FileHandler) completeUpload(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	uploadID := chi.URLParam(r, "uploadID")

	created, err := h.svc.CompleteUpload(r.Context(), userID, uploadID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrUploadSessionNotFound):
			writeError(w, http.StatusNotFound, "upload session not found")
		case errors.Is(err, service.ErrUploadSizeMismatch):
			writeError(w, http.StatusBadRequest, "uploaded bytes do not match declared size")
		case errors.Is(err, service.ErrInvalidFilename):
			writeError(w, http.StatusBadRequest, "invalid filename")
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}

	fr, err := h.newUsernameResolver().toResponse(r.Context(), created)
	if err != nil {
		log.Printf("handler: %v", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusCreated, fr)
}

// abortUpload handles DELETE /api/files/uploads/{uploadID} — discards an
// in-progress upload session (e.g. the user cancels).
func (h *FileHandler) abortUpload(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	uploadID := chi.URLParam(r, "uploadID")
	if err := h.svc.AbortUpload(r.Context(), userID, uploadID); err != nil {
		if errors.Is(err, service.ErrUploadSessionNotFound) {
			writeError(w, http.StatusNotFound, "upload session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// delete handles DELETE /api/files/{id}
func (h *FileHandler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, err := parseFileID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file id")
		return
	}

	if err := h.svc.Delete(r.Context(), userID, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
