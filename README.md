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
  `library/<Game>/<os>/…`, `library/<Game>/dlc/<DLC>/<os>/…` and `library/<Game>/extras/…`
  (when installers in several languages are wanted, each language gets its own
  `library/<Game>/<os>/<lang>/…` folder, since their file names are often identical).
- **Cloud saves** – optionally, a copy of the save games GOG Galaxy keeps in the cloud, for
  the games that have any, in `library/<Game>/saves/…`. A save that is rewritten in the cloud
  is fetched again; the newest version is kept.
- **Automatic updates** – a scheduler re-checks GOG every *N* hours; new versions replace the
  old installer once the new download has completed and verified. A change is confirmed
  against GOG's published checksum before a downloaded file is fetched again, and the
  checksums are re-checked in the background so a silently replaced installer is still found.
- **Robust downloads** – configurable number of parallel downloads, global speed limit,
  resumable `.part` files, MD5 verification against GOG's checksums, automatic retries with
  backoff, free-space check.
- **Web UI** – first-run wizard (authorize → games → platforms → content), dashboard, library
  grid with per-game file lists and selection, sorting by title, status or last update with an
  optional "downloaded first" grouping, downloads page with pause/resume, searchable logs,
  settings.
- **Three download modes** – every game you own, only games you tick in the library, or the
  ticked games plus every game you buy from now on (selected as soon as it shows up).
- **Safe settings changes** – removing a platform (or DLC/extras/cloud saves), deselecting a game or switching
  to "selected games only" asks whether to keep or delete the files already on disk.
- **Single static binary** – Go backend with the React UI embedded, SQLite for state, no cron
  daemon or external services.

## Quick start

```bash
cp .env.example .env      # optional: port, host paths, UID/GID, timezone
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
4. **Content** – choose whether to include DLC, extras and cloud saves, and which installer
   languages.

The first library sync starts immediately afterwards and downloads begin (for selected games
only, in that mode).

### Configuration

Everything about *what* to download (download mode, platforms, languages, DLC/extras/cloud
saves, concurrency, speed limit, check interval, pause) is configured in the web UI under **Settings**
and stored in the database; installations set up before the download mode existed keep
downloading everything. The environment only controls *where* and *how* the process runs:

| Variable | Default | Description |
| --- | --- | --- |
| `DATA_DIR` | `/data` | SQLite database (settings, tokens, file state, logs) |
| `LIBRARY_DIR` | `/library` | Where game folders are created |
| `PORT` | `8080` | HTTP port |
| `LISTEN` | `:$PORT` | Full listen address; set it to bind one interface (`127.0.0.1:8080`) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `STARTUP_SYNC_DELAY_SECONDS` | `15` | Delay before the first scheduled check after boot |
| `MOCK_GOG` | unset | Set to `1` to run against a fake library (demo mode, no GOG account needed) |

## Running with Docker Compose

The repository ships a `docker-compose.yml` that builds the image from source and runs it
as a long-lived service. Host-side settings are read from a `.env` file next to it; copy
`.env.example` to `.env` and edit what you need. Every value is optional.

| `.env` variable | Default | Description |
| --- | --- | --- |
| `PORT` | `8080` | Host port the web UI is published on |
| `DATA_DIR` | `./data` | Host directory mounted at `/data` (database, settings, GOG refresh token) |
| `LIBRARY_DIR` | `./library` | Host directory mounted at `/library` (downloaded installers) |
| `PUID` / `PGID` | `1000` / `1000` | User and group the container runs as |
| `TZ` | `UTC` | Timezone used for log timestamps |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `MOCK_GOG` | `0` | `1` runs the demo library instead of talking to GOG |
| `VERSION` | `dev` | Version string stamped into the binary and used as the image tag |

### Start, stop, update

```bash
cp .env.example .env            # first time only
mkdir -p data library           # or whatever DATA_DIR / LIBRARY_DIR point to
docker compose up -d --build    # build the image and start the service
docker compose logs -f          # follow the log
docker compose ps               # shows the health state (healthy after ~10 s)
docker compose down             # stop; data and library stay on disk
```

To update, pull the new source and rebuild: `git pull && docker compose up -d --build`.
The container restarts automatically after a reboot (`restart: unless-stopped`) and reports
its health through `GET /api/status`, so Docker marks it unhealthy if the process stops
answering.

### Using the published image

Every release is published to GitHub Container Registry as
`ghcr.io/alehel/gogl-watcher` for `linux/amd64` and `linux/arm64`. The compose file already
uses that image name, so you can skip building and pull a release instead:

```bash
VERSION=1.2.3 docker compose pull   # or leave VERSION unset for :latest
VERSION=1.2.3 docker compose up -d  # without --build, so the pulled image is used
```

Tags follow the release version: `1.2.3`, `1.2`, `1` and `latest` all point at the newest
release in that range. Pre-releases (for example `1.3.0-rc.1`) only get their exact tag and
never move `latest`.

### Volumes and permissions

- **`/data`** holds the SQLite database: your settings, the record of every file and its
  version, the logs shown in the UI, and the GOG refresh token. Back this directory up and
  treat it as secret: anyone with the token can act as your GOG account. Disconnecting in
  Settings only removes the token from this app; a copy that has leaked stays valid until you
  end your sessions or change your password on gog.com.
- **`/library`** holds the installers, one folder per game. It is safe to read from, copy or
  serve from the host while the service runs; the app writes new files as `<name>.part` and
  renames them only after the size and checksum have been verified.

The container runs as `PUID:PGID` (default `1000:1000`), so the mounted directories must be
writable by that user. Set `PUID` and `PGID` to the owner of your library directory
(`id -u` / `id -g` on the host) and the downloaded files will be owned by that account.

### Trying it without a GOG account

Set `MOCK_GOG=1` in `.env` and start the stack. The wizard accepts any text as the login
code, the library is filled with sample games, and downloads stream fake data at a few
megabytes per second so every screen can be explored. Point `DATA_DIR` and `LIBRARY_DIR` at
throwaway directories, then set `MOCK_GOG=0` and recreate the container for real use.

### Reverse proxies

The UI and API live on one port with no built-in authentication. Keep it on a private
network or put it behind a reverse proxy that handles login (for example Caddy, nginx or
Traefik with forward auth) before exposing it beyond your LAN.

## How it works

- **Sync** (`internal/library`): fetches the owned product ids and the account game list, then
  each game's download manifest from `api.gog.com` (in "selected games only" mode, only for
  selected games; the rest are just listed). For every game it plans the wanted files
  from your settings (platforms, languages with optional fallback, DLC, extras) and reconciles
  them with the database: new files become *pending*, files that really changed become
  *pending* again (the old file is deleted after the new one succeeds), files that are no
  longer offered are marked *inactive*.
- **Deciding that a file changed** (`internal/library/sync.go`): GOG publishes no build
  identity for offline installers, so three signals are combined, cheapest first. The
  installer's version string and manifest size come with the manifest and cost nothing, but
  they move when nothing changed and stay put when something did. The newest *Galaxy build id*
  of the game (one request per platform, at most every six hours) is a hint that a game was
  rebuilt — Galaxy's chunked builds are not the offline installers and the two are published
  separately, so it only marks a game as worth a closer look. **GOG's published MD5** for the
  file settles it: it is fetched for the files the first two signals point at, and for a slow
  rolling re-check of everything else (25 files per sync, each file at least monthly), so a
  silent replacement is found even for the Linux builds and extras Galaxy never covers. A copy
  whose checksum still matches is kept, however much its version string moved — which is what
  keeps a bumped version from costing a 60 GB re-download.
- **Cloud saves** (`internal/library/saves.go`, `internal/gog/cloud.go`): GOG keeps a game's
  cloud saves in an object store container named by the OAuth client of the game's Galaxy
  build, whose credentials are published in the build manifest (`content-system.gog.com`); a
  game without a Galaxy build has no cloud storage. With cloud saves switched on (for the
  library, or for one game on its page), the sync reads that client once per game and
  remembers it, lists the container (`cloudstorage.gog.com`) with a token issued for the game's
  client, and plans one file per object under `library/<Game>/saves/…`, with the store's hash
  of the object as its version. The downloader fetches them through the same queue as
  installers, checks the stored form against the store's checksum, decompresses it (Galaxy
  uploads saves gzip-compressed) and stamps the file with the time the save was written. A save
  that was rewritten in the cloud is fetched again; only the newest version is kept, as with
  installers. A save that vanished from the cloud is no longer tracked, but a downloaded copy
  stays on disk.
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

`make docker` builds the same image the compose file builds.

### Releasing

Releases are cut by tagging a commit on `main`:

```bash
git checkout main && git pull
git tag v1.2.3
git push origin v1.2.3
```

The `Release` workflow (`.github/workflows/release.yml`) refuses tags whose commit is not on
`main`, runs the test suite, builds the multi-arch image with the tag as the embedded version,
pushes it to `ghcr.io/alehel/gogl-watcher` and creates a GitHub release with generated notes.
A tag containing a hyphen (`v1.3.0-rc.1`) is published as a pre-release.

## Notes and limitations

- GOG has no official public API. The app uses the endpoints and OAuth client of the GOG Galaxy
  desktop client, like other community tools (lgogdownloader, Heroic's gogdl). If GOG changes
  those endpoints the sync will fail until the app is updated; failures are visible in the
  dashboard and the logs.
- Only offline installers, DLC, extras and cloud saves are handled; Galaxy-only "depot" builds
  and patches are not downloaded.
- Cloud saves are a mirror of what the cloud holds now, one version per file. They are never
  uploaded or deleted in the cloud, and nothing is restored into a game automatically: to put a
  backed-up save back, copy it into the game's save folder yourself.
- Removing a game from your GOG account does not delete its folder; it is simply no longer
  shown as owned.

## License

Apache 2.0, see [LICENSE](LICENSE).
