// Package authctx carries the authenticated user's id through a
// request's context.Context. It intentionally contains no JWT parsing or
// validation logic — that belongs to the auth stage's middleware, which
// calls WithUserID after it has verified a token.
package authctx

import "context"

// contextKey is unexported so no other package can collide with or forge
// this context value.
type contextKey struct{}

var userIDKey = contextKey{}

// WithUserID returns a copy of ctx carrying the authenticated user's id.
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserID returns the authenticated user's id stored in ctx, if any. The
// second return value is false when no user id has been set.
func UserID(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}
