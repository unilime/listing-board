# Notes

This file was drafted with Claude Code. The README is the spec and was not edited.

## What was wrong, and what changed

| Where the code disagreed with the README | Change |
| --- | --- |
| `POST /start` set `success` and ticked every step inside the handler. | It now moves a `draft` to `running`, sets a new `runId` and leaves the steps `pending`. Only the worker finishes a sheet. |
| There was no worker. | New process `cmd/worker` (logic in `internal/worker`), built into the same image, run as the `worker` compose service with `restart: "no"`, `STEP_PAUSE: 2s` and the shared `boarddata` volume. It retries the database for 60 s, reads `STEP_PAUSE` as a Go duration (default `2s`), and picks up `running` sheets on every poll, so a restarted worker resumes from the first pending step with the same `runId`. A missing brochure makes the sheet `failed` with exactly `Brochure is missing`. |
| Any signed-in user could read, upload to, start and download another user's sheet by id. | Every route that takes an id now answers 404 for a sheet that is not the caller's (`load` takes the user). |
| Starting a running sheet again made a new `runId`; there was no 409. | Starting a `running` sheet returns it unchanged. A second draft of the same account gets 409. A partial unique index (`jobs_one_running_per_user`) backs this up in Postgres, and a mutex in the API covers the in-memory case. |
| Brochure limit was 8 MiB; type came from the multipart header or the extension; a second upload replaced the first. | 1 MiB limit (exactly 1 MiB is accepted). Type is decided from the bytes (`%PDF-` for PDF; non-empty valid UTF-8 without NUL for text; everything else, an empty file included, is 400). A second upload is 400. The body is streamed, so a rejected file never touches the disk. |
| The file was written to `{DATA_DIR}/{jobID}/{client filename}`, which let `../../x` escape the directory. | The bytes go to `{DATA_DIR}/{userID}/{jobID}/body`. The client's name is kept only in `filename`. |
| The page never refreshed a running sheet. | The page polls `/api/jobs` every second while any sheet is `running`, so the checklist moves by itself. `error` is shown on a failed sheet. |

Smaller fixes in the same area:

- Download sets `Content-Type` from the bytes, adds `nosniff`, and builds `Content-Disposition` from a sanitised last path element.
- The page checks the 1 MiB limit before creating a sheet, no longer limits the file picker to `.pdf`/`.txt` (the spec decides by bytes), and, if the server refuses the brochure after the sheet was created, shows that draft with a file picker instead of leaving an invisible orphan.
- Migrations take a Postgres advisory lock, because the API and the worker now start together.

## Tests

`TestStartMarksTheJobSuccessful` and `TestUploadKeepsTheClientFilename` are rewritten to describe the spec and keep their names:

- the first starts a sheet, asserts `running` + a `runId` + all steps `pending`, then runs a real worker against the same store and asserts `success`, all steps `done`, same `runId`;
- the second asserts `filename` is the client's name and that the bytes are at `{userID}/{jobID}/body` and nowhere else.

`TestListHidesOtherUsersJobs` and `TestStartRequiresASession` are untouched. New tests cover: start without a brochure, idempotent start, 409, a failed sheet staying failed, 404 for another user's sheet on every id route, download, second upload, the size limit, type sniffing, `../../escaped.txt`, and (in `internal/worker`) one save per step, resuming without redoing a done step, a worker stopped mid-run, a missing brochure, and sheets the worker must ignore.

## Choices the README leaves open

- `POST /start` on a `success` or `failed` sheet returns 200 with the sheet unchanged.
- A blank filename is stored as `brochure`; any other name is stored exactly as sent.
- The worker checks that the brochure can be read before every step, the first one included, and saves once per step. After a restart it does the next step at once, without a pause.
- The worker polls every 500 ms and works each running sheet in its own goroutine.
- `board.Store` is unchanged. The worker has its own small interface (`worker.Store`, which adds `ListRunning`), and only the Postgres store and the test doubles implement it.

## A suggestion I rejected

While planning, Claude proposed adding atomic methods to the `board.Store` interface (`StartJob`, `AttachFile`) so the API could do the 409 check and the one-upload rule in a single database call. I rejected it: changing `board.Store` breaks every existing test double that implements it, and the in-memory `mem` in `server_test.go` would have needed its own copy of the locking logic. I kept the interface as it was and used a partial unique index plus a mutex instead.

I checked the result in two ways. `go vet ./...` and `go test -race -count=3 ./...` pass. And with `docker compose up --build` I ran the flow by hand against the real Postgres: start without a brochure (400), upload, start, start again (same `runId`), a second draft while one runs (409), `docker compose kill worker` in the middle of a run (sheet stays `running`, steps stop), `docker compose start worker` (it continues from the first pending step with the same `runId`), a deleted brochure (`failed`, `Brochure is missing`, stays `failed` when started again), and an upload named `../../escaped.txt` (no file outside the data directory).

Not checked: the page was type-checked and built (`npm run build`) but I did not open it in a browser, so the live checklist update is untested by eye.
