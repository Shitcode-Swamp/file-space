// Command api starts the file-space HTTP API server.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"filespace/backend/internal/config"
	"filespace/backend/internal/db"
	"filespace/backend/internal/handler"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/service"
	"filespace/backend/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	storageDir := cfg.StorageDir
	if storageDir == "" {
		storageDir = "data"
	}
	fileStorage, err := storage.NewLocalFileStorage(storageDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	userRepo := repo.NewPostgresUserRepo(database)
	fileRepo := repo.NewPostgresFileRepo(database)

	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	fileSvc := service.NewFileService(fileRepo, fileStorage)
	syncSvc := service.NewSyncService(fileRepo)

	authHandler := handler.NewAuthHandler(authSvc)
	fileHandler := handler.NewFileHandler(fileSvc, userRepo)
	syncHandler := handler.NewSyncHandler(syncSvc)

	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{cfg.CORSAllowedOrigin},
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
	}))

	r.Get("/healthz", handleHealthz)

	// Public auth routes.
	r.Route("/api/auth", authHandler.Routes)

	// Protected routes, behind JWTAuth.
	r.Group(func(pr chi.Router) {
		pr.Use(handler.JWTAuth(cfg.JWTSecret))
		pr.Route("/api/files", fileHandler.Routes)
		pr.Route("/api/sync", syncHandler.Routes)
	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("file-space api listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

// handleHealthz reports liveness. It intentionally returns a plain "ok" body
// (rather than JSON) to match simple uptime-check / load-balancer probe
// conventions.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// writeJSON encodes v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: encode error: %v", err)
	}
}

// errorResponse is the JSON shape returned for API errors.
type errorResponse struct {
	Error string `json:"error"`
}

// writeError writes a JSON error response with the given status code and message.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
