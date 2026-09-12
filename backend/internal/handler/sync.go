package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/authctx"
	"filespace/backend/internal/service"
)

// SyncHandler exposes the /api/sync/* endpoints.
type SyncHandler struct {
	svc *service.SyncService
}

// NewSyncHandler constructs a SyncHandler backed by svc.
func NewSyncHandler(svc *service.SyncService) *SyncHandler {
	return &SyncHandler{svc: svc}
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

// diffRequest is the request body for POST /api/sync/diff.
//
// Scope boundary: REQUIREMENTS.md §6.6 describes a fuller protocol where the
// client also submits its local manifest (name/size/mtime/sha256 per file)
// so the server can detect genuine bidirectional conflicts (§6.2's "hash
// differs" case, resolved per §6.5). This first version of the endpoint
// accepts that shape for forward compatibility but only reads
// LastSyncedVersion; Manifest is decoded and otherwise ignored. Wiring up
// manifest-based conflict detection is left as a future refinement.
type diffRequest struct {
	LastSyncedVersion int64           `json:"lastSyncedVersion"`
	Manifest          []manifestEntry `json:"manifest,omitempty"`
}

// manifestEntry mirrors REQUIREMENTS.md §6.1/§6.6's per-file manifest shape.
// It is accepted but not yet used — see diffRequest's doc comment.
type manifestEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Mtime  string `json:"mtime"`
	SHA256 string `json:"sha256"`
}

type syncActionResponse struct {
	Name          string `json:"name"`
	Action        string `json:"action"`
	RemoteVersion int64  `json:"remoteVersion"`
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

	actions, newVersion, err := h.svc.Diff(r.Context(), userID, req.LastSyncedVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	resp := diffResponse{
		NewVersion: newVersion,
		Actions:    make([]syncActionResponse, 0, len(actions)),
	}
	for _, a := range actions {
		resp.Actions = append(resp.Actions, syncActionResponse{
			Name:          a.Name,
			Action:        a.Action,
			RemoteVersion: a.RemoteVersion,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}
