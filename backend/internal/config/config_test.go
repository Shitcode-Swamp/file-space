package config

import "testing"

// This file runs under plain `go test ./...` (no build tag, no live
// Postgres) — Load reads directly from os.Getenv with no package-level
// state, so t.Setenv is safe to use freely here.

func TestLoad_MissingDatabaseURL(t *testing.T) {
	t.Setenv("JWT_SECRET", "secret")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with no DATABASE_URL: got nil error, want error")
	}
}

func TestLoad_MissingJWTSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/filespace")

	if _, err := Load(); err == nil {
		t.Fatal("Load() with no JWT_SECRET: got nil error, want error")
	}
}

func TestLoad_PortDefaultsWhenUnset(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/filespace")
	t.Setenv("JWT_SECRET", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Port != "8080" {
		t.Fatalf("Port = %q, want %q", cfg.Port, "8080")
	}
}

func TestLoad_CORSAllowedOriginDefaultsWhenUnset(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/filespace")
	t.Setenv("JWT_SECRET", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.CORSAllowedOrigin != defaultCORSAllowedOrigin {
		t.Fatalf("CORSAllowedOrigin = %q, want %q", cfg.CORSAllowedOrigin, defaultCORSAllowedOrigin)
	}
}

func TestLoad_AllFieldsSet(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/filespace")
	t.Setenv("JWT_SECRET", "s3cr3t")
	t.Setenv("STORAGE_DIR", "/var/lib/filespace/storage")
	t.Setenv("PORT", "9090")
	t.Setenv("CORS_ALLOWED_ORIGIN", "https://example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	want := Config{
		DatabaseURL:       "postgres://localhost/filespace",
		JWTSecret:         "s3cr3t",
		StorageDir:        "/var/lib/filespace/storage",
		Port:              "9090",
		CORSAllowedOrigin: "https://example.com",
	}
	if cfg != want {
		t.Fatalf("Load() = %+v, want %+v", cfg, want)
	}
}
