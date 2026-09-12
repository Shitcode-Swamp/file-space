// Package config loads application configuration from environment variables.
package config

import (
	"errors"
	"os"
)

// Config holds all runtime configuration for the API server.
type Config struct {
	DatabaseURL string
	JWTSecret   string
	StorageDir  string
	Port        string
}

// Load reads configuration from the environment. DATABASE_URL and JWT_SECRET
// are required; STORAGE_DIR is optional (empty means "caller decides the
// default"), and PORT defaults to "8080" when unset.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		StorageDir:  os.Getenv("STORAGE_DIR"),
		Port:        os.Getenv("PORT"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("config: DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return Config{}, errors.New("config: JWT_SECRET is required")
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}

	return cfg, nil
}
