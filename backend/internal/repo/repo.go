// Package repo is the data-access layer: it defines the repository
// interfaces the service layer codes against, and provides Postgres
// (sqlx/pgx) implementations of them.
package repo

import (
	"context"
	"errors"

	"filespace/backend/internal/domain"
)

// ErrNotFound is returned by repo methods when the requested row does not
// exist, or (for scoped lookups) does not belong to the requesting user.
// Handlers should treat this as a 404, never leaking whether a row exists
// for a *different* user.
var ErrNotFound = errors.New("repo: not found")

// UserRepo persists and retrieves user accounts.
type UserRepo interface {
	Create(ctx context.Context, username, passwordHash string) (domain.User, error)
	GetByUsername(ctx context.Context, username string) (domain.User, error)
	GetByID(ctx context.Context, id int64) (domain.User, error)
}

// ListParams filters and orders the result of FileRepo.List.
type ListParams struct {
	// UploadedBy restricts the listing to files owned by this user. Every
	// query goes through this field — a user must never be able to see
	// another user's files.
	UploadedBy int64
	// SortEditedByOrder is "", "asc", or "desc". It sorts by the *name* of
	// the user who last edited the file (REQUIREMENTS.md §4), not by the
	// raw edited_by id.
	SortEditedByOrder string
	// Extension filters to files with this extension. "" means all
	// extensions.
	Extension string
}

// FileRepo persists and retrieves file metadata rows.
type FileRepo interface {
	List(ctx context.Context, params ListParams) ([]domain.File, error)
	GetByID(ctx context.Context, uploadedBy, id int64) (domain.File, error)
	Create(ctx context.Context, f domain.File) (domain.File, error)
	Delete(ctx context.Context, uploadedBy, id int64) error
	// ListChangedSince backs the sync/diff endpoint: it returns every file
	// and deletion belonging to uploadedBy with a version greater than
	// sinceVersion, plus the highest version observed (or sinceVersion
	// unchanged if nothing changed).
	ListChangedSince(ctx context.Context, uploadedBy, sinceVersion int64) (changed []domain.File, deleted []domain.Deletion, currentVersion int64, err error)
}
