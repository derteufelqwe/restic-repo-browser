# Restic Repo Browser

A small web interface for looking at [restic](https://restic.net) repositories on local disks:
snapshot history, sizes, a snapshot explorer with downloads, and one scheduled cleanup
(forget, optionally prune) per repository. It ships as a **single binary** (templates, CSS, JavaScript,
htmx and Chart.js are embedded) and needs the `restic` program next to it or on `PATH`.

* Single user, password-only login.
* Everything shown, except the snapshot explorer and downloads, comes from a **cache in SQLite**.
  A *sync* refreshes it once a day (time configurable in *Settings*), after each cleanup run, and on demand
  ("Sync now" on a repository, "Sync all"). Every repository shows when it was last synced and whether that worked.
* **Only one task runs at a time**: a sync (of one or all repositories) or a cleanup run. Starting another while one
  runs is refused with a message and the buttons are disabled; a banner on every page shows what is running and how far it is.
  Scheduled work waits for the running task instead of being dropped.
* All state lives in one SQLite file: repositories, encrypted repository passwords, cached snapshots,
  cleanup jobs, run logs, sessions.

## Download

Ready-made binaries for Linux, Windows and macOS (amd64 and arm64) are on the
[Releases](../../releases) page. Unpack one, install [restic](https://restic.net), and continue below.

Releases are built by GitHub Actions: pushing a tag such as `git tag v0.1.0 && git push origin v0.1.0`
creates the release, the archives and `checksums.txt` automatically (`.github/workflows/release.yml`,
`.goreleaser.yaml`). Every push and pull request runs the tests (`.github/workflows/ci.yml`).

## Run it

```sh
go build -o restic-browser ./cmd/restic-browser     # or: GOOS=linux go build ...

./restic-browser set-password                       # asks for the login password
./restic-browser --listen :8080 --data-dir /var/lib/restic-browser
```

Then open `http://<server>:8080`, sign in and add repositories under *Settings*
(name, folder path, repository password). The path field has a **Browse** button that lists the
folders of the server so you do not have to type the path.

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `127.0.0.1:8080` | Address to listen on. Use `:8080` to serve the whole network. |
| `--data-dir` | `data` | Holds `restic-browser.db`, `secret.key` and restic's cache. |
| `--restic` | `restic` | restic executable (looked up on `PATH`, then next to the binary). |
| `--secure-cookies` | off | Mark the session cookie `Secure` (when served over HTTPS). |
| `--debug` | off | Log every request. |

`secret.key` encrypts the repository passwords inside the database. Keep it next to the database
and back both up together; the database alone does not reveal the passwords. The login password
is stored as a bcrypt hash. The server speaks plain HTTP, so put it behind a TLS proxy if the
network is not trusted.

## Try it with demo data

```sh
go run ./tools/mkdemo -out demo -restic ./restic.exe     # builds real restic repos, takes ~1 minute
./restic-browser --data-dir demo/data --restic ./restic.exe
# sign in with: demo-password
```

## Limits that come from restic

* restic stores no check results, so there is no "last check" figure; the repository page shows when
  the cached data was last updated instead.
* A lock file only records host, user, time and whether it is exclusive, not which command holds it.
  The lock notice therefore says "a lock (usually a running backup)" or "an exclusive lock (forget,
  prune or check)", and it is only as current as the last sync.
* Snapshot size, data added and file count come from the summary restic 0.17+ writes into each
  snapshot. For older snapshots only the size and file count are computed (`restic stats`), "added" stays empty.
* `restic ls` reports no folder sizes, so the explorer shows "–" for folders; folder size is worked out during download.
* Only JPEG, PNG, GIF and WebP files get an image preview.
* A cleanup job that is due while the server is down is skipped, not made up.
* Progress is shown per step and, for a sync of all repositories, per repository. restic gives no machine-readable
  progress for prune, so that step is shown as "in progress" without a percentage.

## Layout

```
cmd/restic-browser     entry point: flags, wiring, set-password
internal/domain        core types and pure logic (freshness state, history buckets, filters)
internal/store         SQLite persistence and migrations
internal/restic        restic command line wrapper
internal/cache         daily and on-demand refresh of the cached data
internal/tasks         the single task slot and progress reporting
internal/jobs          cleanup job scheduler and runner
internal/retention     keep-policy evaluation (mirrors `restic forget`) for the cleanup preview
internal/schedule      cron parsing and descriptions
internal/auth          password login and sessions
internal/web           HTTP handlers, templates and static files (htmx)
tools/mkdemo           demo repository generator (with a ready-made app database)
tools/mktestrepos      plain test repositories to add by hand
mockup/                the static design mockup this UI was built from
```

Tests: `go test ./...` (integration tests use a real `restic` binary and skip themselves when none is found;
set `RESTIC_BIN` to point at one).
