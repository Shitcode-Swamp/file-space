//go:build smoke

package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"filespace/backend/internal/db"
	"filespace/backend/internal/repo"
)

// TestSmokeAuthServiceAgainstLiveDB exercises AuthService's Register/Login/
// Refresh against a real Postgres instance pointed to by SMOKE_DATABASE_URL,
// with real bcrypt hashing and real signed JWTs (no mocks). Excluded from
// normal `go test ./...` via the "smoke" build tag.
func TestSmokeAuthServiceAgainstLiveDB(t *testing.T) {
	dsn := os.Getenv("SMOKE_DATABASE_URL")
	if dsn == "" {
		t.Skip("SMOKE_DATABASE_URL not set")
	}

	sqlxDB, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sqlxDB.Close()

	ctx := context.Background()
	users := repo.NewPostgresUserRepo(sqlxDB)
	svc := NewAuthService(users, "smoke-test-secret")

	username := "auth_smoke_user"
	password := "correct horse battery staple"

	// --- Register ---
	user, err := svc.Register(ctx, username, password)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if user.ID == 0 || user.Username != username {
		t.Fatalf("register: unexpected user %+v", user)
	}
	if user.PasswordHash == password {
		t.Fatalf("register: password was not hashed")
	}

	// Duplicate username must be rejected distinguishably.
	if _, err := svc.Register(ctx, username, "other-password"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("register duplicate: got %v, want ErrUsernameTaken", err)
	}

	// Empty input rejected.
	if _, err := svc.Register(ctx, "", password); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("register empty username: got %v, want ErrInvalidInput", err)
	}
	if _, err := svc.Register(ctx, "someone_else", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("register empty password: got %v, want ErrInvalidInput", err)
	}

	// --- Login: wrong password / wrong username ---
	if _, _, err := svc.Login(ctx, username, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login wrong password: got %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := svc.Login(ctx, "no_such_user", password); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login unknown user: got %v, want ErrInvalidCredentials", err)
	}

	// --- Login: success ---
	access, refresh, err := svc.Login(ctx, username, password)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if access == "" || refresh == "" || access == refresh {
		t.Fatalf("login: expected distinct non-empty tokens, got access=%q refresh=%q", access, refresh)
	}

	assertClaim := func(t *testing.T, tokenString, wantTyp string) {
		t.Helper()
		tok, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
		if err != nil {
			t.Fatalf("parse token: %v", err)
		}
		claims := tok.Claims.(jwt.MapClaims)
		if claims["typ"] != wantTyp {
			t.Fatalf("token typ = %v, want %q", claims["typ"], wantTyp)
		}
		if _, ok := claims["uid"]; !ok {
			t.Fatalf("token missing uid claim")
		}
	}
	assertClaim(t, access, "access")
	assertClaim(t, refresh, "refresh")

	// --- typ confusion must be rejected: access token used as refresh token ---
	if _, err := svc.Refresh(ctx, access); err == nil {
		t.Fatalf("refresh with access token: expected error, got success")
	} else if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("refresh with access token: got %v, want ErrInvalidToken", err)
	}

	// --- Refresh: success, issues a fresh, valid access token ---
	newAccess, err := svc.Refresh(ctx, refresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if newAccess == "" {
		t.Fatalf("refresh: got empty access token")
	}
	assertClaim(t, newAccess, "access")

	// The middleware-side validation logic lives in internal/handler, but we
	// can independently verify the token round-trips through the same
	// secret/algorithm a verifier would use.
	parsed, err := jwt.Parse(newAccess, func(tok *jwt.Token) (any, error) {
		return []byte("smoke-test-secret"), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}))
	if err != nil || !parsed.Valid {
		t.Fatalf("verify new access token: err=%v valid=%v", err, parsed != nil && parsed.Valid)
	}

	// --- Refresh: garbage / malformed token rejected ---
	if _, err := svc.Refresh(ctx, "not-a-jwt"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("refresh garbage token: got %v, want ErrInvalidToken", err)
	}

	// --- Refresh: expired token rejected ---
	expiredClaims := jwt.MapClaims{
		"uid": user.ID,
		"typ": "refresh",
		"iat": jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		"exp": jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
	}
	expiredTok := jwt.NewWithClaims(jwt.SigningMethodHS256, expiredClaims)
	expiredSigned, err := expiredTok.SignedString([]byte("smoke-test-secret"))
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}
	if _, err := svc.Refresh(ctx, expiredSigned); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("refresh expired token: got %v, want ErrInvalidToken", err)
	}

	// --- Refresh: wrong secret rejected ---
	wrongSecretTok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"uid": user.ID,
		"typ": "refresh",
		"iat": jwt.NewNumericDate(time.Now()),
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	wrongSecretSigned, err := wrongSecretTok.SignedString([]byte("a-completely-different-secret"))
	if err != nil {
		t.Fatalf("sign wrong-secret token: %v", err)
	}
	if _, err := svc.Refresh(ctx, wrongSecretSigned); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("refresh wrong-secret token: got %v, want ErrInvalidToken", err)
	}
}
