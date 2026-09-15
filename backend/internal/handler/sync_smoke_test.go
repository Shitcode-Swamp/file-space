//go:build smoke

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/db"
	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/service"
)

// TestSmokeSyncHTTPRoundTrip drives the real POST /api/sync/diff handler
// (behind JWTAuth) end to end against a live Postgres instance pointed to by
// SMOKE_DATABASE_URL: it registers/logs in a user over HTTP, creates and
// deletes files directly through the repo, and confirms the HTTP response
// reports the right actions and newVersion. Excluded from normal
// `go test ./...` via the "smoke" build tag.
func TestSmokeSyncHTTPRoundTrip(t *testing.T) {
	dsn := os.Getenv("SMOKE_DATABASE_URL")
	if dsn == "" {
		t.Skip("SMOKE_DATABASE_URL not set")
	}

	sqlxDB, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sqlxDB.Close()

	const secret = "sync-http-smoke-secret"
	users := repo.NewPostgresUserRepo(sqlxDB)
	files := repo.NewPostgresFileRepo(sqlxDB)
	authSvc := service.NewAuthService(users, secret)
	syncSvc := service.NewSyncService(files)
	authHandler := NewAuthHandler(authSvc)
	syncHandler := NewSyncHandler(syncSvc, users)

	r := chi.NewRouter()
	r.Route("/api/auth", authHandler.Routes)
	r.Route("/api/sync", func(sr chi.Router) {
		sr.Use(JWTAuth(secret))
		syncHandler.Routes(sr)
	})

	doJSON := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatalf("encode body: %v", err)
			}
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	uniq := fmt.Sprintf("sync_http_smoke_user_%d", time.Now().UnixNano())
	password := "correct horse battery staple"

	// Register + login over real HTTP to get a real access token.
	regRec := doJSON(http.MethodPost, "/api/auth/register", "", map[string]string{
		"username": uniq, "password": password,
	})
	if regRec.Code != http.StatusCreated {
		t.Fatalf("register: got %d, body=%s", regRec.Code, regRec.Body.String())
	}
	var regResp struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(regRec.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}

	loginRec := doJSON(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username": uniq, "password": password,
	})
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login: got %d, body=%s", loginRec.Code, loginRec.Body.String())
	}
	var loginResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal(loginRec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}

	// No access token: 401.
	unauthRec := doJSON(http.MethodPost, "/api/sync/diff", "", map[string]int64{"lastSyncedVersion": 0})
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("diff without token: got %d, want 401", unauthRec.Code)
	}

	// Baseline diff: nothing changed yet.
	baseRec := doJSON(http.MethodPost, "/api/sync/diff", loginResp.AccessToken, map[string]int64{"lastSyncedVersion": 0})
	if baseRec.Code != http.StatusOK {
		t.Fatalf("baseline diff: got %d, body=%s", baseRec.Code, baseRec.Body.String())
	}
	var baseResp struct {
		NewVersion int64 `json:"newVersion"`
		Actions    []struct {
			Name          string `json:"name"`
			Action        string `json:"action"`
			RemoteVersion int64  `json:"remoteVersion"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(baseRec.Body.Bytes(), &baseResp); err != nil {
		t.Fatalf("decode baseline diff response: %v", err)
	}
	if len(baseResp.Actions) != 0 || baseResp.NewVersion != 0 {
		t.Fatalf("baseline diff: got %+v, want empty/0", baseResp)
	}

	// Create a file directly via the repo (as if uploaded through the
	// files endpoint, which is a different stage's concern) and confirm
	// the HTTP diff endpoint reports it.
	f, err := files.Create(context.TODO(), domain.File{
		Name: "report.pdf", StorageKey: "key-report-" + uniq, Size: 100, Extension: "pdf",
		UploadedBy: regResp.ID, EditedBy: regResp.ID,
	})
	if err != nil {
		t.Fatalf("create file: %v", err)
	}

	afterCreateRec := doJSON(http.MethodPost, "/api/sync/diff", loginResp.AccessToken, map[string]int64{"lastSyncedVersion": 0})
	if afterCreateRec.Code != http.StatusOK {
		t.Fatalf("diff after create: got %d, body=%s", afterCreateRec.Code, afterCreateRec.Body.String())
	}
	var afterCreateResp struct {
		NewVersion int64 `json:"newVersion"`
		Actions    []struct {
			Name          string `json:"name"`
			Action        string `json:"action"`
			RemoteVersion int64  `json:"remoteVersion"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(afterCreateRec.Body.Bytes(), &afterCreateResp); err != nil {
		t.Fatalf("decode diff after create: %v", err)
	}
	if len(afterCreateResp.Actions) != 1 || afterCreateResp.Actions[0].Name != "report.pdf" ||
		afterCreateResp.Actions[0].Action != "download" || afterCreateResp.Actions[0].RemoteVersion != f.Version ||
		afterCreateResp.NewVersion != f.Version {
		t.Fatalf("diff after create: got %+v, want single download action for report.pdf at version %d", afterCreateResp, f.Version)
	}

	// Delete the file directly via the repo, then confirm delete_local
	// shows up for a client synced at f.Version.
	if err := files.Delete(context.TODO(), regResp.ID, f.ID); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	afterDeleteRec := doJSON(http.MethodPost, "/api/sync/diff", loginResp.AccessToken, map[string]int64{"lastSyncedVersion": f.Version})
	if afterDeleteRec.Code != http.StatusOK {
		t.Fatalf("diff after delete: got %d, body=%s", afterDeleteRec.Code, afterDeleteRec.Body.String())
	}
	var afterDeleteResp struct {
		NewVersion int64 `json:"newVersion"`
		Actions    []struct {
			Name          string `json:"name"`
			Action        string `json:"action"`
			RemoteVersion int64  `json:"remoteVersion"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(afterDeleteRec.Body.Bytes(), &afterDeleteResp); err != nil {
		t.Fatalf("decode diff after delete: %v", err)
	}
	if len(afterDeleteResp.Actions) != 1 || afterDeleteResp.Actions[0].Name != "report.pdf" ||
		afterDeleteResp.Actions[0].Action != "delete_local" {
		t.Fatalf("diff after delete: got %+v, want single delete_local action for report.pdf", afterDeleteResp)
	}
	if afterDeleteResp.NewVersion <= f.Version {
		t.Fatalf("diff after delete: newVersion = %d, want > %d", afterDeleteResp.NewVersion, f.Version)
	}
}
