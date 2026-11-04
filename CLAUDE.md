# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A small board for real-estate listing sheets (Go API + Postgres + React/Vite UI). **`README.md` is the spec**: where code disagrees with it, change the code. It is also a take-home exercise with explicit rules:

- `TestStartMarksTheJobSuccessful` and `TestUploadKeepsTheClientFilename` encode *wrong* current behavior — rewrite them to match the spec and make them pass (never skip, delete, or weaken). Leave `TestListHidesOtherUsersJobs` and `TestStartRequiresASession` as they are.
- Work on a fork, send the fork URL, do not open a PR. A root `NOTES.md` (currently empty/untracked) must name one Cursor/Claude suggestion that was rejected and how the result was checked.

## Commands

```bash
go test ./...                                  # needs Go 1.23, no Postgres (tests use an in-memory store)
go test ./internal/httpapi -run TestName       # single test
docker compose up --build                      # full stack at http://localhost:8080; Postgres on localhost:54329 (board/board/board)
cd web && npm ci && npm run dev                # UI at :5173, proxies /api to :8080
cd web && npm run build                        # tsc --noEmit && vite build (the only UI "lint"/typecheck)
```

`make test` / `make up` wrap the first and third commands. There is no linter config and no UI test setup.

## Architecture

- `cmd/api/main.go` — wires `store.Open` (retries up to 30s waiting for Postgres) into `httpapi.New(store, dataDir, webDir)`. Config is env-only: `DATABASE_URL` (required), `DATA_DIR` (default `data`), `PORT`, `WEB_DIR` (serves the built SPA at `/`).
- `internal/board` — domain types (`Job`/"sheet", `Step`, `User`), the fixed 3-step checklist (`NewChecklist`), ID/token generation, and the **`board.Store` interface**. The HTTP layer depends only on this interface, which is why `server_test.go` can swap in an in-memory `mem` store; any new store method must be added to the interface *and* to both `store.Store` and the test `mem`.
- `internal/httpapi/server.go` — all routes (Go 1.22+ `ServeMux` method patterns), cookie-session auth (`lb_session`, resolved per-handler via `s.current`), JSON helpers, and a CORS shim allowing only `http://localhost:5173`. `s.load(w, r, user)` returns 404 for a sheet that is not the caller's, and every id route must go through it (the spec requires this, file download included). `start` and `upload` take `s.mu` so "one brochure per sheet" and "one running sheet per account" hold under concurrent requests.
- `internal/store` — `database/sql` + pgx. `schema.sql` is embedded (`schema_embed.go`) and executed on every `Open` (idempotent migration, so DDL must use `IF NOT EXISTS`-style statements). Checklist is stored as JSON text in `jobs.checklist`.
- `web/` — single-page React 18 + TS app (`src/App.tsx`), no router/state library.
- Brochures live on disk under `DATA_DIR`, shared between containers through the `boarddata` volume; only the original filename is kept in the DB.

### Worker

`cmd/worker` is a thin main around `internal/worker`, built into the same image and run as the `worker` compose service (`STEP_PAUSE`, default `2s`). The API's `start` only moves a draft to `running` (new `runId`, steps pending); the worker alone advances steps (`board.Job.Advance`) and sets `success`/`failed` (`Brochure is missing`). It polls `ListRunning` and re-reads each sheet before every step, so a restarted worker resumes from the first pending step with the same `runId`. `worker.Store` is a separate, smaller interface than `board.Store` (adds `ListRunning`) on purpose: `board.Store` is kept unchanged so existing test doubles still compile. The one-running-sheet-per-account rule is also enforced by the partial unique index `jobs_one_running_per_user` in `schema.sql` (a violation surfaces as `board.ErrDuplicate` from `UpdateJob`, answered with 409).

Brochures are validated by their bytes (1 MiB limit, `%PDF-` or non-empty NUL-free UTF-8) and stored at `{DATA_DIR}/{userID}/{jobID}/body`; the client's filename lives only in the sheet's `filename` field. The UI polls `/api/jobs` every second while any sheet is `running`. `NOTES.md` records the design decisions and how they were verified.
