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

// SortField selects which column FileRepo.List orders by. The zero value
// ("") means SortByName.
type SortField string

const (
	SortByName       SortField = "name"
	SortByCreatedAt  SortField = "createdAt"
	SortByModifiedAt SortField = "modifiedAt"
	SortByUploadedBy SortField = "uploadedBy"
	SortByEditedBy   SortField = "editedBy"
)

// ListParams filters, orders, and paginates the result of FileRepo.List.
type ListParams struct {
	// UploadedBy restricts the listing to files owned by this user. Every
	// query goes through this field — a user must never be able to see
	// another user's files.
	UploadedBy int64
	// Extension filters to files with this extension. "" means all
	// extensions.
	Extension string
	// SortField is one of the SortBy* constants above; "" defaults to
	// SortByName.
	SortField SortField
	// SortOrder is "asc" or "desc"; "" defaults to "asc".
	SortOrder string
	// Limit caps the number of files a single List call returns. Callers
	// that don't care about pagination (internal tests, the sync/diff
	// endpoint's own listing) can leave this at its zero value to get every
	// matching file back in one call, with hasMore always false. Anything
	// reachable from the HTTP API always sets a positive Limit — see
	// handler.FileHandler's list handler.
	Limit int
	// Offset skips this many files (after sorting/filtering) before the
	// page begins. Only meaningful alongside a positive Limit.
	Offset int
}

// FileRepo persists and retrieves file metadata rows.
type FileRepo interface {
	// List returns one page of files matching params. hasMore reports
	// whether more files exist beyond what was returned (always false when
	// params.Limit is 0, since that means "no limit").
	List(ctx context.Context, params ListParams) (files []domain.File, hasMore bool, err error)
	GetByID(ctx context.Context, uploadedBy, id int64) (domain.File, error)
	Create(ctx context.Context, f domain.File) (domain.File, error)
	Delete(ctx context.Context, uploadedBy, id int64) error
	// ListChangedSince backs the sync/diff endpoint: it returns every file
	// and deletion belonging to uploadedBy with a version greater than
	// sinceVersion, plus the highest version observed (or sinceVersion
	// unchanged if nothing changed).
	ListChangedSince(ctx context.Context, uploadedBy, sinceVersion int64) (changed []domain.File, deleted []domain.Deletion, currentVersion int64, err error)
}
