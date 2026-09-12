// Package service contains the business logic layer (sorting/filtering
// rules, sync-diff logic, permission checks) decoupled from HTTP and SQL.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"filespace/backend/internal/domain"
	"filespace/backend/internal/repo"
)

// Errors returned by AuthService. Handlers map these to HTTP status codes.
var (
	// ErrInvalidInput is returned when username or password is empty (or
	// otherwise fails basic validation) on Register.
	ErrInvalidInput = errors.New("service: invalid input")
	// ErrUsernameTaken is returned by Register when the username already
	// exists. It is distinguishable from other errors so the handler can
	// respond 409 Conflict instead of 500.
	ErrUsernameTaken = errors.New("service: username already taken")
	// ErrInvalidCredentials is returned by Login when the username does not
	// exist or the password does not match. The two cases are deliberately
	// not distinguished, to avoid leaking which usernames are registered.
	ErrInvalidCredentials = errors.New("service: invalid username or password")
	// ErrInvalidToken is returned by Refresh when the supplied refresh token
	// is malformed, expired, wrongly signed, or not a refresh token.
	ErrInvalidToken = errors.New("service: invalid or expired refresh token")
)

const (
	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 7 * 24 * time.Hour

	claimTyp          = "typ"
	typAccess         = "access"
	typRefresh        = "refresh"
	claimUserID       = "uid"
	pgUniqueViolation = "23505"
)

// AuthService implements registration, login, and access-token refresh.
//
// Tradeoff (intentional, for this phase): refresh tokens are stateless
// JWTs, not stored server-side. There is no revocation list or refresh-token
// table, so a leaked refresh token remains valid for its full 7-day life and
// cannot be invalidated early (e.g. on logout or password change). This
// matches REQUIREMENTS.md §5.2's suggestion of JWTs but skips the "stored
// server-side" half for simplicity; revisit if logout/revocation becomes a
// requirement.
type AuthService struct {
	users     repo.UserRepo
	jwtSecret []byte
}

// NewAuthService constructs an AuthService backed by users, signing tokens
// with jwtSecret.
func NewAuthService(users repo.UserRepo, jwtSecret string) *AuthService {
	return &AuthService{users: users, jwtSecret: []byte(jwtSecret)}
}

// Register creates a new user account with a bcrypt-hashed password.
func (s *AuthService) Register(ctx context.Context, username, password string) (domain.User, error) {
	if username == "" || password == "" {
		return domain.User{}, ErrInvalidInput
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return domain.User{}, fmt.Errorf("service: hash password: %w", err)
	}

	user, err := s.users.Create(ctx, username, string(hash))
	if err != nil {
		if isUniqueViolation(err) {
			return domain.User{}, ErrUsernameTaken
		}
		return domain.User{}, fmt.Errorf("service: register: %w", err)
	}
	return user, nil
}

// isUniqueViolation reports whether err wraps a Postgres unique_violation
// (23505), which for the users table means a duplicate username.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation
	}
	return false
}

// Login verifies username/password and, on success, issues a fresh access
// and refresh token pair.
func (s *AuthService) Login(ctx context.Context, username, password string) (accessToken, refreshToken string, err error) {
	user, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return "", "", ErrInvalidCredentials
		}
		return "", "", fmt.Errorf("service: login: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", "", ErrInvalidCredentials
	}

	accessToken, err = s.issueToken(user.ID, typAccess, accessTokenTTL)
	if err != nil {
		return "", "", fmt.Errorf("service: issue access token: %w", err)
	}
	refreshToken, err = s.issueToken(user.ID, typRefresh, refreshTokenTTL)
	if err != nil {
		return "", "", fmt.Errorf("service: issue refresh token: %w", err)
	}
	return accessToken, refreshToken, nil
}

// Refresh validates refreshToken (must be a non-expired, correctly signed
// token with typ=="refresh") and issues a new access token for its subject.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (accessToken string, err error) {
	userID, err := s.parseToken(refreshToken, typRefresh)
	if err != nil {
		return "", ErrInvalidToken
	}

	// Confirm the user still exists; a deleted account shouldn't be able to
	// mint new access tokens off an old refresh token.
	if _, err := s.users.GetByID(ctx, userID); err != nil {
		return "", ErrInvalidToken
	}

	accessToken, err = s.issueToken(userID, typAccess, accessTokenTTL)
	if err != nil {
		return "", fmt.Errorf("service: issue access token: %w", err)
	}
	return accessToken, nil
}

// issueToken builds and signs an HS256 JWT for userID with the given typ
// claim and expiry.
func (s *AuthService) issueToken(userID int64, typ string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		claimUserID: userID,
		claimTyp:    typ,
		"iat":       jwt.NewNumericDate(now),
		"exp":       jwt.NewNumericDate(now.Add(ttl)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

// parseToken validates tokenString's signature and expiry, checks that its
// "typ" claim equals wantTyp, and returns the embedded user id.
func (s *AuthService) parseToken(tokenString, wantTyp string) (int64, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}))
	if err != nil || !token.Valid {
		return 0, fmt.Errorf("service: parse token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return 0, errors.New("service: unexpected claims type")
	}

	typ, _ := claims[claimTyp].(string)
	if typ != wantTyp {
		return 0, fmt.Errorf("service: wrong token type: want %q got %q", wantTyp, typ)
	}

	return userIDFromClaims(claims)
}

// userIDFromClaims extracts the uid claim, which JSON-decodes as float64.
func userIDFromClaims(claims jwt.MapClaims) (int64, error) {
	raw, ok := claims[claimUserID]
	if !ok {
		return 0, errors.New("service: missing uid claim")
	}
	f, ok := raw.(float64)
	if !ok {
		return 0, errors.New("service: uid claim not a number")
	}
	return int64(f), nil
}
