# file-space

A simplified Google Drive: browse, upload, download, and delete files in a personal remote workspace, with folder sync from a desktop client. Self-hosted on a home Ubuntu server behind a Cloudflare Tunnel.

Full spec (DB schema, API shape, sync protocol, deployment layout) lives in [REQUIREMENTS.md](./REQUIREMENTS.md). Repo conventions are in [CLAUDE.md](./CLAUDE.md).

## Stack

| Component | Tech |
|---|---|
| Backend  | Go — `handler → service → repo` layering, no ORM, `chi`/`net/http` |
| Frontend | React + Vite + TypeScript, TanStack Query + TanStack Table |
| Desktop  | Swift (SwiftUI), packaged with Swift Package Manager |
| Database | PostgreSQL, migrations via `golang-migrate` |
| Deploy   | Docker Compose (API + Postgres + `cloudflared`) on an Ubuntu box |

## Repo layout

```text
backend/     Go module — cmd/api, internal/{handler,service,repo,storage,domain,db,config,authctx}
frontend/    React (Vite + TypeScript) web client
desktop/     Swift package (SwiftUI) macOS client
deploy/      docker-compose.yml, cloudflared config
migrations/  golang-migrate SQL files
docs/        supporting documentation
```

## Getting started

All commands below are also available via the top-level `Makefile` — run `make help` for the full list.

### Backend

```bash
cd backend
go build ./...
go vet ./...
go test ./...
```

Or via Make, from the repo root:

```bash
make db-up        # start local Postgres in Docker
make migrate-up    # apply migrations
make backend-run   # go run ./cmd/api against local Postgres
```

### Frontend

```bash
cd frontend
npm install
npm run dev
npm run build
npm run lint
```

### Desktop

```bash
cd desktop
swift build
swift run
swift test   # needs full Xcode.app, not just the Command Line Tools
```

### Local database

```bash
make db-up       # start/create the 'filespace-db' Postgres container on :5432
make migrate-up  # apply migrations/
make migrate-down # roll back the last migration
make db-down     # stop and remove the container
```

## Configuration

Secrets (`DATABASE_URL`, `JWT_SECRET`, `POSTGRES_PASSWORD`, `TUNNEL_TOKEN`) are supplied via environment variables / a gitignored `.env` file — never committed. See `deploy/docker-compose.yml` for the variables each service expects.

## Deployment

The full stack (API, Postgres, `cloudflared`) runs together via Docker Compose on the Ubuntu host:

```bash
cd deploy
cp ../.env.example ../.env   # fill in real secrets
docker compose --env-file ../.env up -d --build
```

`cloudflared` exposes the API on a public hostname via an outbound-only tunnel, without opening inbound ports.
