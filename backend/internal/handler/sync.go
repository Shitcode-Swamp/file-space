package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/authctx"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/service"
)

// SyncHandler exposes the /api/sync/* endpoints.
type SyncHandler struct {
	svc   *service.SyncService
	users repo.UserRepo
}

// NewSyncHandler constructs a SyncHandler backed by svc, resolving a
// conflict action's RemoteEditedBy id to a username via users (mirroring
// FileHandler's usernameResolver).
func NewSyncHandler(svc *service.SyncService, users repo.UserRepo) *SyncHandler {
	return &SyncHandler{svc: svc, users: users}
}

// Routes registers the sync endpoints onto r, so the Integration stage can
// mount this handler without knowing its internals, e.g.:
//
//	r.Route("/api/sync", syncHandler.Routes)
//
// All routes here require authentication (authctx.UserID); the Integration
// stage is expected to wrap this mount point with handler.JWTAuth.
func (h *SyncHandler) Routes(r chi.Router) {
	r.Post("/diff", h.diff)
}

// diffRequest is the request body for POST /api/sync/diff. Manifest is the
// client's current local file list (REQUIREMENTS.md §6.1); omitting it (or
// sending it empty) just means Diff falls back to reporting every
// server-side change as "download", the same as before this endpoint could
// detect conflicts.
type diffRequest struct {
	LastSyncedVersion int64           `json:"lastSyncedVersion"`
	Manifest          []manifestEntry `json:"manifest,omitempty"`
}

// manifestEntry mirrors REQUIREMENTS.md §6.1/§6.6's per-file manifest shape.
// Size and Mtime are accepted for forward compatibility with the documented
// wire shape but aren't used server-side — conflict detection only needs
// Name and SHA256 (see service.ManifestEntry).
type manifestEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Mtime  string `json:"mtime"`
	SHA256 string `json:"sha256"`
}

// conflictDetails is only present on a syncActionResponse whose Action is
// "conflict" — everything REQUIREMENTS.md §6.5's resolution UI needs to
// show the incoming server-side version alongside the client's own local
// one.
type conflictDetails struct {
	RemoteSize       int64     `json:"remoteSize"`
	RemoteModifiedAt time.Time `json:"remoteModifiedAt"`
	RemoteEditedBy   string    `json:"remoteEditedBy"`
}

type syncActionResponse struct {
	Name          string           `json:"name"`
	Action        string           `json:"action"`
	RemoteVersion int64            `json:"remoteVersion"`
	Conflict      *conflictDetails `json:"conflict,omitempty"`
}

type diffResponse struct {
	NewVersion int64                `json:"newVersion"`
	Actions    []syncActionResponse `json:"actions"`
}

func (h *SyncHandler) diff(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or invalid access token")
		return
	}

	var req diffRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	manifest := make([]service.ManifestEntry, 0, len(req.Manifest))
	for _, m := range req.Manifest {
		manifest = append(manifest, service.ManifestEntry{Name: m.Name, SHA256: m.SHA256})
	}

	actions, newVersion, err := h.svc.Diff(r.Context(), userID, req.LastSyncedVersion, manifest...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	resolver := &usernameResolver{users: h.users, cache: make(map[int64]string)}
	resp := diffResponse{
		NewVersion: newVersion,
		Actions:    make([]syncActionResponse, 0, len(actions)),
	}
	for _, a := range actions {
		out := syncActionResponse{
			Name:          a.Name,
			Action:        a.Action,
			RemoteVersion: a.RemoteVersion,
		}
		if a.Action == service.ActionConflict {
			editedBy, err := resolver.resolve(r.Context(), a.RemoteEditedBy)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			out.Conflict = &conflictDetails{
				RemoteSize:       a.RemoteSize,
				RemoteModifiedAt: a.RemoteModifiedAt,
				RemoteEditedBy:   editedBy,
			}
		}
		resp.Actions = append(resp.Actions, out)
	}

	writeJSON(w, http.StatusOK, resp)
}
