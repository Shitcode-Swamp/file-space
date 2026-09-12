# Remote File Folder Client (Simplified Google Drive)

## Overview

The system is an application that allows users to interact with a remote folder containing files — a simplified, scalable equivalent of Google Drive.

## 1. Authentication and User Workspace

- The application must provide **authentication on a remote server**.
- After successful authentication, the user is taken to their own personal workspace — a **virtual disk**.

The user must be able to:

- view files available in their remote folder;
- see file attributes:
  - file name;
  - creation date and time;
  - modification date and time;
  - name of the user who uploaded the file;
  - name of the user who last edited the file;
- upload files to the remote server;
- download files from the remote server;
- delete files;
- synchronize a selected local folder with the corresponding remote folder.

## 2. File Content Display

When a user clicks on a file, its contents should be displayed if it has one of the following types:

- **`.java`** — display source code as text;
- **`.png`** — display the image.

For all other file types, structured displaying contents is not required.

## 3. Table Columns

- The user must be able to **show or hide** all file-information columns:
  - creation date;
  - modification date;
  - uploader;
  - editor.
- The **file-name column must always remain visible**.

## 4. Operations

### Sorting

- Sort files by the **name of the user who last edited the file**.
- Ascending order.
- Descending order.

### Filtering

- Display **all files**.
- Display only files of the following types:
  - `.cs`
  - `.jpg`

### Example Table

| Name         | Created | Modified | Uploaded by | Edited by |
|--------------|---------|----------|-------------|-----------|
| `Program.cs` | ...     | ...      | Alice       | Bob       |
| `image.jpg`  | ...     | ...      | Bob         | Alice     |

Users can sort by **Edited by** (ascending/descending) and filter by **All / `.cs` / `.jpg`**.

## 5. Architecture

Target stack: **Go** backend, **React** web client, **Swift** desktop client, self-hosted on an **Ubuntu home server** and exposed publicly via **Cloudflare Tunnel (`cloudflared`)**.

### 5.1 Component overview

```text
                 ┌────────────────┐        ┌──────────────────────┐
                 │  React (web)   │        │  Swift (macOS/SwiftUI)│
                 │  browser client│        │  desktop client        │
                 └───────┬────────┘        └──────────┬────────────┘
                         │ HTTPS (JSON/REST)           │ HTTPS (JSON/REST)
                         └──────────────┬───────────────┘
                                        ▼
                          ┌───────────────────────────┐
                          │      Cloudflare Tunnel     │   ← runs on the
                          │        (cloudflared)       │     Ubuntu server
                          └──────────────┬──────────────┘
                                         ▼
                          ┌───────────────────────────┐
                          │      Go API server         │
                          │  (net/http + chi router)   │
                          │  handlers → services →     │
                          │  repositories               │
                          └───────┬───────────┬─────────┘
                                  │           │
                     ┌────────────┘           └────────────┐
                     ▼                                      ▼
           ┌──────────────────┐                  ┌───────────────────┐
           │   PostgreSQL      │                  │   File storage      │
           │  users, files,    │                  │  local disk now,    │
           │  metadata          │                  │  S3/R2-compatible   │
           │                    │                  │  later (behind an   │
           │                    │                  │  interface)          │
           └──────────────────┘                  └───────────────────┘
```

Everything (Go API, Postgres, cloudflared) runs on the same Ubuntu box via **Docker Compose**. `cloudflared` is what makes the server reachable on a public hostname without opening ports or dealing with dynamic DNS — it establishes an outbound-only tunnel from your home server to Cloudflare's edge.

### 5.2 Backend (Go)

- **Router**: `chi` (or `net/http` 1.22+ `ServeMux` if you want zero dependencies) — thin HTTP layer.
- **Layering**: `handler → service → repository`, so business logic (sorting/filtering rules, sync-diff logic, permission checks) is decoupled from HTTP and SQL.
- **DB access**: `sqlc` (generate typed Go from SQL) or `sqlx` — avoid a heavy ORM; the schema is small.
- **Migrations**: `golang-migrate`.
- **Auth**: JWT access tokens (short-lived) + refresh tokens stored server-side (DB) or as httpOnly cookies for the web client; the Swift client can store tokens in the macOS Keychain.
- **Password hashing**: `bcrypt` or `argon2id`.
- **File storage abstraction**:

  ```go
  type FileStorage interface {
      Save(ctx context.Context, key string, r io.Reader) error
      Open(ctx context.Context, key string) (io.ReadCloser, error)
      Delete(ctx context.Context, key string) error
  }
  // LocalFileStorage now, S3Storage (or Cloudflare R2, via the S3-compatible API) later —
  // same interface, so swapping is a config change, not a rewrite.
  ```

- **API surface** (same shape as before, framework-agnostic):

  ```text
  POST   /api/auth/login
  POST   /api/auth/register
  POST   /api/auth/refresh

  GET    /api/files?sort=editedBy&order=asc&extension=cs
  GET    /api/files/{id}
  GET    /api/files/{id}/content     # text/binary preview for .java / .png
  GET    /api/files/{id}/download
  POST   /api/files                  # upload
  DELETE /api/files/{id}

  POST   /api/sync/diff              # client sends local manifest, server returns actions
  ```

### 5.3 Web client (React)

- **Tooling**: Vite + React + TypeScript.
- **File table**: TanStack Table (sorting/filtering/column visibility map directly onto its built-in APIs) + TanStack Query for data fetching/caching against the Go API.
- **Preview**:
  - `.java` → fetch as text, render with a syntax highlighter (`shiki` or `react-syntax-highlighter`).
  - `.png` → render the download/content URL directly in an `<img>`.
- **Sync from the browser is inherently limited** (no persistent background filesystem access, no filesystem watching): at best you can use the File System Access API for a one-shot folder pick + manual "sync now" button. Treat the **web client as upload/download/browse-first**, and treat **continuous background sync as a native-client feature** (Swift, see below).

### 5.4 Desktop client (Swift)

- **UI**: SwiftUI app (macOS), reusing the same REST API as the web client.
- **Networking**: `URLSession` + `Codable` models generated/matched to the Go API's JSON shapes.
- **Token storage**: macOS Keychain (`Security` framework), not `UserDefaults`.
- **This is where real synchronization belongs**:
  - Watch the chosen local folder with `FSEvents` (or `DispatchSource` file monitoring) for changes.
  - Periodically (or on file-system events) compute local manifest: `{name, size, mtime, sha256}`.
  - `POST /api/sync/diff` with that manifest; server replies with per-file actions (`upload` / `download` / `conflict` / `noop`) by comparing against stored metadata.
  - Execute uploads/downloads, and surface conflicts to the user (e.g. "both changed" → keep-both / overwrite choice), same conflict model as described in section 6.
  - Can run as a regular app or a menu-bar (`NSStatusItem`) background agent for Dropbox-style continuous sync.
- If you later want sync on Windows/Linux too, the diff protocol is already server-side and stack-agnostic — you'd just need another native watcher client (e.g. a small Go CLI using `fsnotify`), without touching the API.

### 5.5 Data storage on the home server

- **Postgres**: single container, metadata only (`users`, `files` tables as in section 6's schema). A named Docker volume for `pgdata`.
- **Files**: a bind-mounted directory on the Ubuntu host (e.g. `/srv/filespace/storage/user-{id}/...`), mounted into the Go API container. Back this directory up separately from the Docker volumes (e.g. periodic `rsync`/`restic` to another disk or cloud storage) since it's the one thing that isn't easily reproducible.
- Because storage is behind the `FileStorage` interface, moving to Cloudflare R2 (S3-compatible, and a natural pairing since you're already on Cloudflare) later is a config/env change, not a code rewrite.

### 5.6 Deployment: Ubuntu + Docker Compose + cloudflared

```yaml
# docker-compose.yml (sketch)
services:
  api:
    build: ./backend
    environment:
      - DATABASE_URL=postgres://...
      - STORAGE_DIR=/data/storage
      - JWT_SECRET=...
    volumes:
      - ./storage:/data/storage
    depends_on: [db]

  db:
    image: postgres:16
    environment:
      - POSTGRES_PASSWORD=...
    volumes:
      - pgdata:/var/lib/postgresql/data

  cloudflared:
    image: cloudflare/cloudflared:latest
    command: tunnel run
    environment:
      - TUNNEL_TOKEN=...   # from `cloudflared tunnel create`
    depends_on: [api]

volumes:
  pgdata:
```

- Create the tunnel once with `cloudflared tunnel create filespace`, map a hostname (e.g. `drive.yourdomain.com`) to the `api` service in the tunnel's ingress config, and Cloudflare handles TLS termination and public DNS — no router port-forwarding needed.
- The React app can be served either as static files from the same Go binary (`embed.FS`) behind the same tunnel hostname, or as a separate `web` container/route (`/` → static SPA, `/api/*` → Go backend).
- Optional hardening: put the hostname behind **Cloudflare Access** (Zero Trust) for an extra login gate in front of your own app auth — useful since this will be reachable from the public internet on a home connection.

### 5.7 Suggested repo layout

```text
filespace/
  backend/          # Go module: cmd/api, internal/{handler,service,repo,storage}
  frontend/         # React app
  desktop/          # Swift/Xcode project
  deploy/
    docker-compose.yml
    cloudflared/config.yml
  migrations/       # golang-migrate SQL files
```

Given the home-server, single-node reality, "scalability" here mainly means: **stateless API containers + storage/DB behind interfaces**, so if you ever outgrow one box you can move Postgres and file storage to managed/external services without touching client code — not that you need multiple API replicas on day one.

## 6. Synchronization

Synchronization is a **manifest diff** between the local folder and the server's record of that folder, run periodically and on file-system events. This is the most involved part of the system, so it's worth designing deliberately rather than as an afterthought.

### 6.1 What gets compared

For every file, both sides track a small manifest entry:

```text
{ name, size, mtime, sha256 }
```

- The **Swift desktop client** computes this for every file in the watched local folder.
- The **server** already has the equivalent stored per file in the `files` table, alongside `uploaded_by` / `edited_by`.

### 6.2 The four outcomes

| Local | Remote | Action |
|---|---|---|
| exists | doesn't exist | upload |
| doesn't exist | exists | download |
| exists, hash matches | exists | no-op |
| exists, hash differs | exists | conflict |

### 6.3 Handling deletions: sync cursors

A file missing locally could mean "never downloaded yet" or "the user deleted it" — absence alone can't distinguish these. The fix is a **sync cursor (version) per client**:

- The server keeps a monotonically increasing version number per file (or a global append-only change log: `file_id, change_type, version`).
- Each client remembers the last version it successfully synced (`last_synced_version`), stored locally (e.g. in a small SQLite/plist/UserDefaults sidecar on the Swift client).
- On each sync, the client sends its manifest **and** `last_synced_version`. The server replies with everything changed since that version, including explicit `deleted` entries — so a real delete propagates instead of being mistaken for "never downloaded."

### 6.4 Trigger mechanism

- **Swift desktop client**: `FSEvents` watches the folder for local changes in real time → debounce (e.g. 1–2s, to avoid firing mid-write) → compute diff for changed files only → sync. Additionally poll the server on an interval (e.g. every 30–60s) to catch remote-side changes made from another device or the web client.
- **React web client**: no persistent watcher (browsers can't background-watch a folder); a manual **"Sync now"** button using the File System Access API for a one-shot manifest comparison. Treat the web client as browse/upload/download-first, and continuous background sync as a native-client (Swift) feature.

### 6.5 Conflict resolution

When both sides changed the same file since the last synced version, don't silently overwrite. Surface the conflict to the user with options:

- **Keep local** (overwrite remote);
- **Keep remote** (overwrite local);
- **Keep both** (rename the incoming/outgoing copy, e.g. `Program (conflict copy, 2026-09-11).cs`), matching the pattern Dropbox/Google Drive use.

### 6.6 Protocol

```text
POST /api/sync/diff
Request:
{
  "lastSyncedVersion": 42,
  "manifest": [
    { "name": "Program.cs", "size": 1240, "mtime": "...", "sha256": "..." },
    ...
  ]
}

Response:
{
  "newVersion": 47,
  "actions": [
    { "name": "image.jpg", "action": "download" },
    { "name": "old.txt",   "action": "delete_local" },
    { "name": "Notes.java","action": "conflict", "remoteVersion": 45 }
  ]
}
```

The client executes the returned actions (upload/download/delete locally), resolves conflicts per 6.5, then stores `newVersion` as its new `lastSyncedVersion` for the next round.

### 6.7 Portability note

Because the diff logic lives entirely server-side behind `POST /api/sync/diff`, adding sync support for another platform later (Windows/Linux) only requires a native folder-watcher client (e.g. a small Go CLI using `fsnotify`) speaking the same protocol — no backend changes needed.
