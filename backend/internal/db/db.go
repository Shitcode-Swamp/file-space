// Package db wires up the database connection pool used by the repo layer.
package db

import (
	"fmt"

	// Registers the "pgx" driver with database/sql, which sqlx builds on.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

// Connect opens a connection pool to dsn using the pgx stdlib driver and
// verifies connectivity with a Ping before returning.
func Connect(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return db, nil
}
