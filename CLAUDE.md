# file-space

Simplified Google Drive: Go API, React web client, Swift (macOS) desktop client, self-hosted on Ubuntu behind Cloudflare Tunnel.

Full spec and architecture: see `REQUIREMENTS.md`. Read it before making design decisions — it already covers the DB schema, API shape, sync protocol, and deployment layout.

## Repo layout (planned)

```text
backend/    Go module — cmd/api, internal/{handler,service,repo,storage}
frontend/   React (Vite + TypeScript)
desktop/    Swift/Xcode project (SwiftUI)
deploy/     docker-compose.yml, cloudflared config
migrations/ golang-migrate SQL files
```

## Conventions

- **Backend**: layered `handler → service → repo`; no ORM — `sqlc` or `sqlx` over raw SQL. File storage always goes through the `FileStorage` interface (see REQUIREMENTS.md §5.2), never direct filesystem calls from handlers.
- **Frontend**: TanStack Query for server state, TanStack Table for the file list (sort/filter/column-visibility map to its built-in APIs).
- **Sync protocol**: single source of truth is `POST /api/sync/diff` (REQUIREMENTS.md §6). Any new sync-capable client must speak this protocol rather than inventing its own diffing.
- **Secrets**: JWT secret, DB creds, Cloudflare tunnel token — via environment variables / `.env` (gitignored), never committed.

## Commands

- **Backend** (from `backend/`): `go build ./...`, `go vet ./...`, `go test ./...`.
- **Frontend** (from `frontend/`): `npm run dev`, `npm run build`, `npm run lint`.
- **Desktop**: _to be filled in once the Xcode project is scaffolded._
