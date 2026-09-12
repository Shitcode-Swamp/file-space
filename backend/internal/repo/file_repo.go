package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"

	"filespace/backend/internal/domain"
)

// PostgresFileRepo is a Postgres-backed implementation of FileRepo.
type PostgresFileRepo struct {
	db *sqlx.DB
}

// NewPostgresFileRepo constructs a PostgresFileRepo.
func NewPostgresFileRepo(db *sqlx.DB) *PostgresFileRepo {
	return &PostgresFileRepo{db: db}
}

// compile-time check that PostgresFileRepo satisfies FileRepo.
var _ FileRepo = (*PostgresFileRepo)(nil)

type fileRow struct {
	ID         int64          `db:"id"`
	Name       string         `db:"name"`
	StorageKey string         `db:"storage_key"`
	Size       int64          `db:"size"`
	Extension  sql.NullString `db:"extension"`
	CreatedAt  sql.NullTime   `db:"created_at"`
	ModifiedAt sql.NullTime   `db:"modified_at"`
	UploadedBy int64          `db:"uploaded_by"`
	EditedBy   int64          `db:"edited_by"`
	Version    int64          `db:"version"`
}

func (r fileRow) toDomain() domain.File {
	return domain.File{
		ID:         r.ID,
		Name:       r.Name,
		StorageKey: r.StorageKey,
		Extension:  r.Extension.String,
		Size:       r.Size,
		CreatedAt:  r.CreatedAt.Time,
		ModifiedAt: r.ModifiedAt.Time,
		UploadedBy: r.UploadedBy,
		EditedBy:   r.EditedBy,
		Version:    r.Version,
	}
}

type deletionRow struct {
	FileID     int64        `db:"file_id"`
	Name       string       `db:"name"`
	StorageKey string       `db:"storage_key"`
	Version    int64        `db:"version"`
	DeletedAt  sql.NullTime `db:"deleted_at"`
}

func (r deletionRow) toDomain() domain.Deletion {
	return domain.Deletion{
		FileID:     r.FileID,
		Name:       r.Name,
		StorageKey: r.StorageKey,
		Version:    r.Version,
		DeletedAt:  r.DeletedAt.Time,
	}
}

// List returns files owned by params.UploadedBy, optionally filtered by
// extension and ordered by the *name* of the user who last edited the
// file.
func (r *PostgresFileRepo) List(ctx context.Context, params ListParams) ([]domain.File, error) {
	query := `
		SELECT f.id, f.name, f.storage_key, f.size, f.extension,
		       f.created_at, f.modified_at, f.uploaded_by, f.edited_by, f.version
		FROM files f
		JOIN users editor ON editor.id = f.edited_by
		WHERE f.uploaded_by = $1`
	args := []any{params.UploadedBy}

	if params.Extension != "" {
		args = append(args, params.Extension)
		query += fmt.Sprintf(" AND f.extension = $%d", len(args))
	}

	switch params.SortEditedByOrder {
	case "asc":
		query += " ORDER BY editor.username ASC, f.id ASC"
	case "desc":
		query += " ORDER BY editor.username DESC, f.id ASC"
	default:
		query += " ORDER BY f.id ASC"
	}

	var rows []fileRow
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("repo: list files: %w", err)
	}

	files := make([]domain.File, 0, len(rows))
	for _, row := range rows {
		files = append(files, row.toDomain())
	}
	return files, nil
}

// GetByID returns the file with the given id, scoped to uploadedBy so a
// user can never fetch another user's file by guessing an id.
func (r *PostgresFileRepo) GetByID(ctx context.Context, uploadedBy, id int64) (domain.File, error) {
	const query = `
		SELECT id, name, storage_key, size, extension,
		       created_at, modified_at, uploaded_by, edited_by, version
		FROM files
		WHERE id = $1 AND uploaded_by = $2`

	var row fileRow
	if err := r.db.GetContext(ctx, &row, query, id, uploadedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.File{}, ErrNotFound
		}
		return domain.File{}, fmt.Errorf("repo: get file by id: %w", err)
	}
	return row.toDomain(), nil
}

// Create inserts f and returns the row with its DB-assigned id, version,
// and timestamps.
func (r *PostgresFileRepo) Create(ctx context.Context, f domain.File) (domain.File, error) {
	const query = `
		INSERT INTO files (name, storage_key, size, extension, uploaded_by, edited_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, storage_key, size, extension,
		          created_at, modified_at, uploaded_by, edited_by, version`

	var row fileRow
	if err := r.db.GetContext(ctx, &row, query,
		f.Name, f.StorageKey, f.Size, f.Extension, f.UploadedBy, f.EditedBy,
	); err != nil {
		return domain.File{}, fmt.Errorf("repo: create file: %w", err)
	}
	return row.toDomain(), nil
}

// Delete removes the file with the given id (scoped to uploadedBy),
// recording a tombstone in file_deletions with a fresh version from the
// shared sequence, atomically.
func (r *PostgresFileRepo) Delete(ctx context.Context, uploadedBy, id int64) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repo: delete file: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const selectQuery = `
		SELECT id, name, storage_key, size, extension,
		       created_at, modified_at, uploaded_by, edited_by, version
		FROM files
		WHERE id = $1 AND uploaded_by = $2
		FOR UPDATE`

	var row fileRow
	if err := tx.GetContext(ctx, &row, selectQuery, id, uploadedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("repo: delete file: select: %w", err)
	}

	const insertDeletionQuery = `
		INSERT INTO file_deletions (file_id, uploaded_by, name, storage_key)
		VALUES ($1, $2, $3, $4)`
	if _, err := tx.ExecContext(ctx, insertDeletionQuery, row.ID, uploadedBy, row.Name, row.StorageKey); err != nil {
		return fmt.Errorf("repo: delete file: insert tombstone: %w", err)
	}

	const deleteQuery = `DELETE FROM files WHERE id = $1 AND uploaded_by = $2`
	if _, err := tx.ExecContext(ctx, deleteQuery, id, uploadedBy); err != nil {
		return fmt.Errorf("repo: delete file: delete row: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repo: delete file: commit: %w", err)
	}
	return nil
}

// ListChangedSince returns everything that changed for uploadedBy since
// sinceVersion: files whose version increased (created or edited) and
// deletions recorded since then, plus the highest version observed across
// both sets (or sinceVersion unchanged if nothing changed). It backs the
// sync/diff endpoint.
func (r *PostgresFileRepo) ListChangedSince(ctx context.Context, uploadedBy, sinceVersion int64) ([]domain.File, []domain.Deletion, int64, error) {
	const filesQuery = `
		SELECT id, name, storage_key, size, extension,
		       created_at, modified_at, uploaded_by, edited_by, version
		FROM files
		WHERE uploaded_by = $1 AND version > $2
		ORDER BY version ASC`

	var fileRows []fileRow
	if err := r.db.SelectContext(ctx, &fileRows, filesQuery, uploadedBy, sinceVersion); err != nil {
		return nil, nil, 0, fmt.Errorf("repo: list changed files: %w", err)
	}

	const deletionsQuery = `
		SELECT file_id, name, storage_key, version, deleted_at
		FROM file_deletions
		WHERE uploaded_by = $1 AND version > $2
		ORDER BY version ASC`

	var deletionRows []deletionRow
	if err := r.db.SelectContext(ctx, &deletionRows, deletionsQuery, uploadedBy, sinceVersion); err != nil {
		return nil, nil, 0, fmt.Errorf("repo: list changed deletions: %w", err)
	}

	changed := make([]domain.File, 0, len(fileRows))
	currentVersion := sinceVersion
	for _, row := range fileRows {
		changed = append(changed, row.toDomain())
		if row.Version > currentVersion {
			currentVersion = row.Version
		}
	}

	deleted := make([]domain.Deletion, 0, len(deletionRows))
	for _, row := range deletionRows {
		deleted = append(deleted, row.toDomain())
		if row.Version > currentVersion {
			currentVersion = row.Version
		}
	}

	return changed, deleted, currentVersion, nil
}
