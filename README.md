# gogl-watcher

A self-hosted service that keeps an **offline copy of your GOG library**.
It runs as a single Docker container, downloads the offline installers of every game
you own (or only of the games you pick) into one folder per game, and checks GOG on a
schedule so new games and updated installers are fetched automatically. A built-in web UI shows what has been
downloaded, what is pending, what failed, the download queue and the logs, and lets you
limit bandwidth and concurrency.

## Features

- **Full or selected library** – download every game you own, or only the games you tick in
  the library (nothing is selected until you do). Installers (Windows / macOS / Linux, your
  choice), owned DLC and optionally extras (soundtracks, manuals, artbooks…) are organised as
  `library/<Game>/<os>/…`, `library/<Game>/dlc/<DLC>/<os>/…` and `library/<Game>/extras/…`.
- **Automatic updates** – a scheduler re-checks GOG every *N* hours; new versions replace the
  old installer once the new download has completed and verified.
- **Robust downloads** – configurable number of parallel downloads, global speed limit,
  resumable `.part` files, MD5 verification against GOG's checksums, automatic retries with
  backoff, free-space check.
- **Web UI** – first-run wizard (authorize → games → platforms → content), dashboard, library
  grid with per-game file lists and selection, downloads page with pause/resume, searchable logs,
  settings.
- **Safe settings changes** – removing a platform (or DLC/extras), deselecting a game or switching
  to "selected games only" asks whether to keep or delete the files already on disk.
- **Single static binary** – Go backend with the React UI embedded, SQLite for state, no cron
  daemon or external services.

## Quick start

```bash
mkdir -p data library
docker compose up -d --build
```

Open <http://localhost:8080>. On first start the setup wizard walks you through:

1. **Authorize** – click the link to open GOG's login page (the same flow the GOG Galaxy
   client uses), log in, then copy the URL of the blank page you land on (or just the
   `code=` value) and paste it into the app. Your password never touches the app; only the
   resulting refresh token is stored in `/data`.
2. **Games** – download every game you own, or only games you select. With "only selected",
   no game is selected to begin with: tick games in the library afterwards and their installers
   are fetched right away. Large libraries usually want this.
3. **Platforms** – pick which installer platforms you want (at least one).
4. **Content** – choose whether to include DLC and extras, and which installer languages.

The first library sync starts immediately afterwards and downloads begin (for selected games
only, in that mode).

### Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `DATA_DIR` | `/data` | SQLite database (settings, tokens, file state, logs) |
| `LIBRARY_DIR` | `/library` | Where game folders are created |
| `PORT` | `8080` | HTTP port |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `STARTUP_SYNC_DELAY_SECONDS` | `15` | Delay before the first scheduled check after boot |
| `MOCK_GOG` | unset | Set to `1` to run against a fake library (demo mode, no GOG account needed) |

Everything else (download mode, platforms, languages, DLC/extras, concurrency, speed limit,
check interval, pause) is configured in the UI under **Settings** and stored in the database.
Installations set up before the download mode existed keep downloading everything.

The container runs as UID/GID 1000; make sure the mounted `data` and `library` directories are
writable by that user (or run with `user: "<uid>:<gid>"` in the compose file).

## How it works

- **Sync** (`internal/library`): fetches the owned product ids and the account game list, then
  each game's download manifest from `api.gog.com` (in "selected games only" mode, only for
  selected games; the rest are just listed). For every game it plans the wanted files
  from your settings (platforms, languages with optional fallback, DLC, extras) and reconciles
  them with the database: new files become *pending*, files whose version or size changed
  become *pending* again (the old file is deleted after the new one succeeds), files that are no
  longer offered are marked *inactive*.
- **Downloader** (`internal/downloader`): a worker pool fills up to *N* parallel transfers from
  the pending queue, resolving GOG's time-limited CDN links at download time. A shared token
  bucket enforces the speed limit. Transfers write to `<file>.part`, resume with HTTP ranges,
  verify MD5 when GOG provides a checksum, then rename into place.
- **Scheduler** (`internal/scheduler`): runs a sync after boot and every *check interval* hours;
  the UI can trigger one at any time.
- **Server** (`internal/server`): JSON API under `/api` (documented in [`docs/api.md`](docs/api.md))
  and the embedded SPA from `web/`.

## Development

Requirements: Go 1.26+, Node 22+.

```bash
# backend tests
go test ./...

# frontend (dev server on :5173 proxies /api to :8080)
cd web && npm install && npm run dev

# backend against a fake GOG library
make mock

# full build (frontend embedded into the binary)
make build
```

`make docker` builds the image; `docker compose up` does the same via the `build:` key.

## Notes and limitations

- GOG has no official public API. The app uses the endpoints and OAuth client of the GOG Galaxy
  desktop client, like other community tools (lgogdownloader, Heroic's gogdl). If GOG changes
  those endpoints the sync will fail until the app is updated; failures are visible in the
  dashboard and the logs.
- Only offline installers, DLC and extras are handled; Galaxy-only "depot" builds and patches
  are not downloaded.
- Removing a game from your GOG account does not delete its folder; it is simply no longer
  shown as owned.

## License

Apache 2.0, see [LICENSE](LICENSE).
