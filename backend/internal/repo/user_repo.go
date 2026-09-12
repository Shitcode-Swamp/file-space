package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"

	"filespace/backend/internal/domain"
)

// PostgresUserRepo is a Postgres-backed implementation of UserRepo.
type PostgresUserRepo struct {
	db *sqlx.DB
}

// NewPostgresUserRepo constructs a PostgresUserRepo.
func NewPostgresUserRepo(db *sqlx.DB) *PostgresUserRepo {
	return &PostgresUserRepo{db: db}
}

// compile-time check that PostgresUserRepo satisfies UserRepo.
var _ UserRepo = (*PostgresUserRepo)(nil)

type userRow struct {
	ID           int64  `db:"id"`
	Username     string `db:"username"`
	PasswordHash string `db:"password_hash"`
}

func (r userRow) toDomain() domain.User {
	return domain.User{
		ID:           r.ID,
		Username:     r.Username,
		PasswordHash: r.PasswordHash,
	}
}

// Create inserts a new user and returns the row with its DB-assigned id.
func (r *PostgresUserRepo) Create(ctx context.Context, username, passwordHash string) (domain.User, error) {
	const query = `
		INSERT INTO users (username, password_hash)
		VALUES ($1, $2)
		RETURNING id, username, password_hash`

	var row userRow
	if err := r.db.GetContext(ctx, &row, query, username, passwordHash); err != nil {
		return domain.User{}, fmt.Errorf("repo: create user: %w", err)
	}
	return row.toDomain(), nil
}

// GetByUsername looks up a user by their unique username.
func (r *PostgresUserRepo) GetByUsername(ctx context.Context, username string) (domain.User, error) {
	const query = `
		SELECT id, username, password_hash
		FROM users
		WHERE username = $1`

	var row userRow
	if err := r.db.GetContext(ctx, &row, query, username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, ErrNotFound
		}
		return domain.User{}, fmt.Errorf("repo: get user by username: %w", err)
	}
	return row.toDomain(), nil
}

// GetByID looks up a user by id.
func (r *PostgresUserRepo) GetByID(ctx context.Context, id int64) (domain.User, error) {
	const query = `
		SELECT id, username, password_hash
		FROM users
		WHERE id = $1`

	var row userRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.User{}, ErrNotFound
		}
		return domain.User{}, fmt.Errorf("repo: get user by id: %w", err)
	}
	return row.toDomain(), nil
}
