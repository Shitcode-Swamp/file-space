// Package domain holds the plain data structures shared across the
// service and repo layers, independent of HTTP or SQL concerns.
package domain

import "time"

// User is an application account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// File is a single file's metadata record, as persisted in the "files"
// table. Content bytes live in the FileStorage backend, keyed by
// StorageKey.
type File struct {
	ID         int64
	Name       string
	StorageKey string
	Extension  string
	Size       int64
	CreatedAt  time.Time
	ModifiedAt time.Time
	UploadedBy int64
	EditedBy   int64
	Version    int64
	// SHA256 is the hex-encoded content hash computed at upload time (see
	// FileService.Create), used by the sync/diff protocol to detect
	// conflicts (REQUIREMENTS.md §6.2). Empty for rows created before this
	// column existed (migrations/0004) -- Diff treats an empty hash as "not
	// enough information to call a conflict," not as a mismatch.
	SHA256 string
}

// Deletion is a tombstone record left behind when a file is deleted, so
// that sync clients can distinguish "deleted" from "never downloaded" by
// comparing against their last synced version (REQUIREMENTS.md §6.3).
type Deletion struct {
	FileID     int64
	Name       string
	StorageKey string
	Version    int64
	DeletedAt  time.Time
}
