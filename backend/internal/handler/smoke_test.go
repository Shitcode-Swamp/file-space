//go:build smoke

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"filespace/backend/internal/authctx"
	"filespace/backend/internal/db"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/service"
)

// TestSmokeAuthHTTPRoundTrip drives the real HTTP handlers (register, login,
// refresh) plus the JWTAuth middleware end to end against a live Postgres
// instance pointed to by SMOKE_DATABASE_URL. Excluded from normal
// `go test ./...` via the "smoke" build tag.
func TestSmokeAuthHTTPRoundTrip(t *testing.T) {
	dsn := os.Getenv("SMOKE_DATABASE_URL")
	if dsn == "" {
		t.Skip("SMOKE_DATABASE_URL not set")
	}

	sqlxDB, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sqlxDB.Close()

	const secret = "http-smoke-secret"
	users := repo.NewPostgresUserRepo(sqlxDB)
	svc := service.NewAuthService(users, secret)
	authHandler := NewAuthHandler(svc)

	r := chi.NewRouter()
	r.Route("/api/auth", authHandler.Routes)

	var gotUserID int64
	r.With(JWTAuth(secret)).Get("/api/protected", func(w http.ResponseWriter, req *http.Request) {
		id, ok := authctx.UserID(req.Context())
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotUserID = id
		w.WriteHeader(http.StatusOK)
	})

	doJSON := func(method, path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatalf("encode body: %v", err)
			}
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// --- register ---
	rec := doJSON(http.MethodPost, "/api/auth/register", map[string]string{
		"username": "http_smoke_user",
		"password": "sup3r-secret",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var regResp struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if regResp.ID == 0 || regResp.Username != "http_smoke_user" {
		t.Fatalf("register: unexpected response %+v", regResp)
	}

	// --- register duplicate -> 409 ---
	rec = doJSON(http.MethodPost, "/api/auth/register", map[string]string{
		"username": "http_smoke_user",
		"password": "different",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("register duplicate: status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}

	// --- register bad input -> 400 ---
	rec = doJSON(http.MethodPost, "/api/auth/register", map[string]string{"username": "", "password": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("register bad input: status = %d, want 400", rec.Code)
	}

	// --- login bad credentials -> 401 ---
	rec = doJSON(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "http_smoke_user",
		"password": "wrong",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login bad creds: status = %d, want 401", rec.Code)
	}

	// --- login success ---
	rec = doJSON(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "http_smoke_user",
		"password": "sup3r-secret",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var loginResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginResp.AccessToken == "" || loginResp.RefreshToken == "" {
		t.Fatalf("login: missing tokens in %+v", loginResp)
	}

	// --- protected route without token -> 401 ---
	req := httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("protected no token: status = %d, want 401", rec2.Code)
	}

	// --- protected route with access token -> 200, authctx populated ---
	req = httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.Header.Set("Authorization", "Bearer "+loginResp.AccessToken)
	rec2 = httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("protected with access token: status = %d, want 200", rec2.Code)
	}
	if gotUserID != regResp.ID {
		t.Fatalf("protected route: authctx uid = %d, want %d", gotUserID, regResp.ID)
	}

	// --- protected route with refresh token instead of access -> 401 ---
	req = httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.Header.Set("Authorization", "Bearer "+loginResp.RefreshToken)
	rec2 = httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("protected with refresh token: status = %d, want 401", rec2.Code)
	}

	// --- refresh -> new access token works on protected route ---
	rec = doJSON(http.MethodPost, "/api/auth/refresh", map[string]string{
		"refreshToken": loginResp.RefreshToken,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var refreshResp struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refreshResp); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	if refreshResp.AccessToken == "" {
		t.Fatalf("refresh: empty access token")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/protected", nil)
	req.Header.Set("Authorization", "Bearer "+refreshResp.AccessToken)
	rec2 = httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("protected with refreshed access token: status = %d, want 200", rec2.Code)
	}

	// --- refresh with garbage token -> 401 ---
	rec = doJSON(http.MethodPost, "/api/auth/refresh", map[string]string{"refreshToken": "garbage"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh garbage: status = %d, want 401", rec.Code)
	}
}
