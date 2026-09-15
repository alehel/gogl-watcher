# gogl-watcher

Keeps an offline copy of your GOG library. It downloads the offline installers (and
optionally DLC, extras and cloud saves) of every game you own or only the games you pick,
re-checks GOG on a schedule so new games and updated installers are fetched automatically,
and serves a web UI to manage it all.

- One folder per game under `/library`: `<Game>/<os>/<language>/…`, `<Game>/dlc/…`, `<Game>/extras/…`,
  `<Game>/saves/…`.
- Updated installers replace the old file only after the new download has verified (MD5
  against GOG's checksum). Resumable downloads, parallel transfers, global speed limit.
- What to download (mode, platforms, languages, DLC/extras/saves, concurrency, speed limit,
  check interval) is set in the web UI, not in the environment.
- No built-in authentication. Keep it on a private network or behind a reverse proxy.

Images: `ghcr.io/alehel/gogl-watcher` (`linux/amd64`, `linux/arm64`). Tags: `latest`,
`1`, `1.2`, `1.2.3`.

## docker-compose.yml

```yaml
services:
  gogl-watcher:
    image: ghcr.io/alehel/gogl-watcher:latest
    container_name: gogl-watcher
    restart: unless-stopped
    user: "1000:1000"
    ports:
      - "8080:8080"
    environment:
      TZ: Europe/Oslo
      LOG_LEVEL: info
    volumes:
      - /srv/gogl-watcher/data:/data
      - /mnt/games/gog:/library
```

Then open `http://localhost:8080` and follow the wizard: authorize with GOG (log in on
GOG's page, paste back the URL or `code=` value you land on; only the refresh token is
stored), choose all games or selected games, pick platforms, pick content.

## Parameters

| Parameter | Default | What to set |
| --- | --- | --- |
| `user` | `1000:1000` | UID:GID that owns the two mounted directories (`id -u`, `id -g`). Downloaded files are owned by it. |
| `ports` | `8080:8080` | Host port for the web UI. Change the left side only. |
| `/data` volume | – | SQLite database: settings, file state, logs and the GOG refresh token. Small; back it up and keep it private, the token gives full access to your GOG account. |
| `/library` volume | – | Where the installers go. Safe to read or serve from the host while running; files are written as `.part` and renamed when verified. |
| `TZ` | `UTC` | Timezone for log timestamps. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `PORT` | `8080` | Port inside the container. Only change if `8080` clashes with `network_mode: host`. |
| `LISTEN` | `:$PORT` | Full listen address, e.g. `127.0.0.1:8080` to bind one interface. Overrides `PORT`. |
| `STARTUP_SYNC_DELAY_SECONDS` | `15` | Seconds to wait after start before the first GOG check. |
| `MOCK_GOG` | `0` | `1` runs against a fake library with no GOG account; any text works as the login code. Use throwaway volumes. |
| `DATA_DIR` / `LIBRARY_DIR` | `/data` / `/library` | Paths inside the container. Leave as is and mount volumes there. |

To build the image from source instead, run `make docker` and point `image:` at
`gogl-watcher:latest`.

## Notes

- GOG has no public API; the app uses the same endpoints as the GOG Galaxy client, like
  lgogdownloader and Heroic's gogdl. If GOG changes them, syncs fail until the app is updated.
- Only offline installers, DLC, extras and cloud saves are handled, not Galaxy depot builds.
- Cloud saves are a one-way mirror of the newest version in the cloud. Nothing is uploaded,
  deleted or restored automatically.
- Removing a game from your account does not delete its folder.
- Health check: `GET /api/status`. API reference: [docs/api.md](docs/api.md). Internals:
  [docs/how-it-works.md](docs/how-it-works.md).

## Development

Go 1.26+, Node 22+.

```bash
go test ./...                              # backend tests
cd web && npm install && npm run dev       # UI dev server on :5173, proxies /api to :8080
make mock                                  # backend against a fake GOG library
make build                                 # binary with the UI embedded
make docker                                # local image tagged gogl-watcher:latest
```

Releases: see [docs/releasing.md](docs/releasing.md).

## License

Apache 2.0, see [LICENSE](LICENSE).
