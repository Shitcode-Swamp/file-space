package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"filespace/backend/internal/authctx"
)

// JWTAuth returns chi-compatible middleware that requires a valid, non-expired
// access token (JWT, HS256, signed with secret, "typ" claim == "access") in
// the Authorization header as "Bearer <token>". On success it stores the
// token's user id in the request context via authctx.WithUserID before
// calling next; on failure it writes a 401 JSON error and does not call next.
func JWTAuth(secret string) func(http.Handler) http.Handler {
	key := []byte(secret)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, err := userIDFromRequest(r, key)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "missing or invalid access token")
				return
			}

			ctx := authctx.WithUserID(r.Context(), userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// userIDFromRequest extracts and validates the bearer access token from r,
// returning the authenticated user's id.
func userIDFromRequest(r *http.Request, key []byte) (int64, error) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return 0, errors.New("handler: missing bearer token")
	}
	tokenString := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if tokenString == "" {
		return 0, errors.New("handler: empty bearer token")
	}

	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}))
	if err != nil || !token.Valid {
		return 0, fmt.Errorf("handler: invalid token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return 0, errors.New("handler: unexpected claims type")
	}

	typ, _ := claims["typ"].(string)
	if typ != "access" {
		return 0, fmt.Errorf("handler: wrong token type: %q", typ)
	}

	uidRaw, ok := claims["uid"]
	if !ok {
		return 0, errors.New("handler: missing uid claim")
	}
	uidFloat, ok := uidRaw.(float64)
	if !ok {
		return 0, errors.New("handler: uid claim not a number")
	}

	return int64(uidFloat), nil
}
