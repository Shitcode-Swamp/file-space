package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"filespace/backend/internal/authctx"
)

// This file runs under plain `go test ./...` (no build tag, no live
// Postgres) — JWTAuth takes only a secret string and returns http.Handler
// middleware, so it's exercised here with hand-built JWTs (using the same
// github.com/golang-jwt/jwt/v5 library the middleware itself uses) rather
// than a real AuthService.

const testSecret = "test-secret-do-not-use-in-prod"

// signToken builds and signs a JWT with the given claims using HS256 and
// secret, failing the test on error.
func signToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// validAccessClaims returns a claim set for a fresh, valid access token for
// uid, suitable as a baseline that individual tests mutate.
func validAccessClaims(uid float64) jwt.MapClaims {
	return jwt.MapClaims{
		"typ": "access",
		"uid": uid,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
}

// noneAlgToken hand-builds a well-formed JWT string using the "none"
// algorithm and an empty signature — a classic forgery attempt that must be
// rejected by JWTAuth's WithValidMethods([]string{"HS256"}) restriction
// regardless of claim contents.
func noneAlgToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	return header + "." + payload + "."
}

// newRecordingHandler returns a handler that records whether it was called
// and, if so, the user id observed via authctx.UserID.
func newRecordingHandler() (http.Handler, *bool, *int64) {
	called := false
	var seenUID int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if id, ok := authctx.UserID(r.Context()); ok {
			seenUID = id
		}
		w.WriteHeader(http.StatusOK)
	})
	return h, &called, &seenUID
}

func doAuthedRequest(t *testing.T, secret, authHeader string) (*httptest.ResponseRecorder, bool, int64) {
	t.Helper()
	next, called, seenUID := newRecordingHandler()
	mw := JWTAuth(secret)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	return rec, *called, *seenUID
}

func TestJWTAuth_NoAuthorizationHeader(t *testing.T) {
	rec, called, _ := doAuthedRequest(t, testSecret, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("next handler was called, want not called")
	}
}

func TestJWTAuth_MissingBearerPrefix(t *testing.T) {
	token := signToken(t, testSecret, validAccessClaims(7))
	rec, called, _ := doAuthedRequest(t, testSecret, token) // no "Bearer " prefix
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("next handler was called, want not called")
	}
}

func TestJWTAuth_ValidAccessToken_CallsNextWithUserID(t *testing.T) {
	token := signToken(t, testSecret, validAccessClaims(42))
	rec, called, seenUID := doAuthedRequest(t, testSecret, "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !called {
		t.Fatal("next handler was not called, want called")
	}
	if seenUID != 42 {
		t.Fatalf("downstream saw uid = %d, want %d", seenUID, 42)
	}
}

func TestJWTAuth_WrongSigningSecret(t *testing.T) {
	token := signToken(t, "some-other-secret", validAccessClaims(1))
	rec, called, _ := doAuthedRequest(t, testSecret, "Bearer "+token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("next handler was called, want not called")
	}
}

func TestJWTAuth_UnexpectedAlgorithm_None(t *testing.T) {
	token := noneAlgToken(t, validAccessClaims(1))
	rec, called, _ := doAuthedRequest(t, testSecret, "Bearer "+token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("next handler was called, want not called")
	}
}

func TestJWTAuth_ExpiredToken(t *testing.T) {
	claims := jwt.MapClaims{
		"typ": "access",
		"uid": float64(1),
		"exp": time.Now().Add(-time.Hour).Unix(),
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
	}
	token := signToken(t, testSecret, claims)
	rec, called, _ := doAuthedRequest(t, testSecret, "Bearer "+token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("next handler was called, want not called")
	}
}

func TestJWTAuth_RefreshTokenRejected(t *testing.T) {
	claims := validAccessClaims(1)
	claims["typ"] = "refresh"
	token := signToken(t, testSecret, claims)
	rec, called, _ := doAuthedRequest(t, testSecret, "Bearer "+token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("next handler was called, want not called (refresh token used as access token)")
	}
}

func TestJWTAuth_MissingUIDClaim(t *testing.T) {
	claims := validAccessClaims(0)
	delete(claims, "uid")
	token := signToken(t, testSecret, claims)
	rec, called, _ := doAuthedRequest(t, testSecret, "Bearer "+token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("next handler was called, want not called")
	}
}

func TestJWTAuth_NonNumericUIDClaim(t *testing.T) {
	claims := validAccessClaims(0)
	claims["uid"] = "not-a-number"
	token := signToken(t, testSecret, claims)
	rec, called, _ := doAuthedRequest(t, testSecret, "Bearer "+token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("next handler was called, want not called")
	}
}
