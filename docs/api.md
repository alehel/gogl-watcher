# gogl-watcher HTTP API

All endpoints live under `/api`, speak JSON, and are served by the Go backend on the
same origin as the web UI. Errors use the shape `{"error": "human readable message"}`
with an appropriate 4xx/5xx status. Timestamps are RFC 3339 strings or `null`.

## Status

### `GET /api/status`

```json
{
  "version": "0.1.0",
  "setup_complete": false,
  "setup_step": "auth",            // "auth" | "platforms" | "content" | "done"
  "authenticated": false,
  "auth_error": null,               // string when the stored refresh token stopped working
  "user": null,                     // { "id": "123", "username": "name" } when authenticated
  "sync": {
    "running": false,
    "phase": "",                    // "" | "listing" | "details" | "reconciling"
    "games_total": 0,               // games discovered in this sync
    "games_done": 0,                // games whose details have been processed in this sync
    "last_started_at": null,
    "last_finished_at": null,
    "last_error": null,
    "next_run_at": null
  },
  "downloads": {
    "paused": false,
    "active": 0,
    "queued": 0,
    "speed_bps": 0
  },
  "library": {
    "games": 0, "complete": 0, "pending": 0, "downloading": 0,
    "partial": 0, "error": 0, "unavailable": 0,
    "bytes_total": 0, "bytes_done": 0
  },
  "disk": { "library_dir": "/library", "free_bytes": 0, "total_bytes": 0 }
}
```

`setup_step` tells the UI which wizard step is next: `auth` (no valid GOG token yet),
`platforms` (no platform chosen yet), `content` (DLC/extras choice not made yet),
`done` (setup finished). While `setup_complete` is false every UI route should redirect
to the wizard.

## Authentication

### `GET /api/auth/url`
`{ "url": "https://auth.gog.com/auth?..." }` – open in the user's browser. After login GOG
redirects to `https://embed.gog.com/on_login_success?origin=client&code=...`. The user
copies either the whole URL or just the `code` value.

### `POST /api/auth/code`
Body `{ "code": "<code or full redirect url>" }`. Exchanges it for tokens.
200 → `{ "user": { "id": "123", "username": "name" } }`; 400 with `error` on failure.

### `GET /api/auth`
`{ "authenticated": true, "user": {...}|null, "expires_at": "...", "error": null }`

### `POST /api/auth/logout`
Removes stored tokens. 204.

## Setup

### `POST /api/setup/complete`
Marks setup as complete. Requires authentication, at least one platform, and an explicit
content choice (see settings). 200 `{ "ok": true }` or 409 `{ "error": "..." }`.
Triggers the first library sync.

## Settings

### `GET /api/settings`

```json
{
  "platforms": ["windows"],              // any of "windows" | "mac" | "linux"; empty until chosen
  "languages": ["en"],                   // GOG language codes, e.g. "en", "de", "fr"
  "language_fallback": true,             // download another language if none of the chosen exist
  "content_chosen": false,               // becomes true once the setup wizard stored the choice
  "include_dlc": true,
  "include_extras": false,
  "max_concurrent_downloads": 2,         // 1..8
  "speed_limit_kbps": 0,                 // kilobytes per second, 0 = unlimited
  "check_interval_hours": 6,             // 1..168
  "downloads_paused": false
}
```

### `POST /api/settings/preview`
Body: the full settings object. Returns which already-tracked files would stop being
wanted if these settings were applied:

```json
{
  "needs_confirmation": true,
  "removed": { "files": 12, "bytes": 123456789, "downloaded_files": 10, "downloaded_bytes": 100000000 },
  "reasons": ["platform:linux", "extras", "dlc", "language:de"]
}
```

### `PUT /api/settings`
Body `{ "settings": {...}, "on_removed": "keep" | "delete" | null }`.
If the change drops previously-wanted files and `on_removed` is null → 409
`{ "error": "confirmation_required", ... same fields as preview ... }`.
With `"keep"` the files stay on disk and are shown as *inactive*; with `"delete"` they are
removed from disk and from the game's file list. 200 returns the stored settings.

Language codes available for the picker: `GET /api/settings/languages` →
`{ "languages": [ { "code": "en", "name": "English" }, ... ] }`.

## Library

### `GET /api/games?q=&status=&sort=`
`status` filter: one of the game statuses below; `sort`: `title` (default) | `status` | `updated`.

```json
{ "games": [ {
  "id": 1207658924,
  "title": "The Witcher",
  "slug": "the_witcher",
  "image": "https://images.gog-statics.com/...jpg",
  "folder": "The Witcher",
  "works_on": { "windows": true, "mac": true, "linux": false },
  "owned": true,
  "status": "complete",   // "complete" | "downloading" | "pending" | "partial" | "error" | "unavailable" | "unsynced"
  "files_total": 3, "files_done": 3,
  "bytes_total": 1234, "bytes_done": 1234,
  "progress": 1.0,
  "last_synced_at": "...", "updated_at": "..."
} ] }
```

Status meaning: `unsynced` – details not fetched yet; `unavailable` – GOG offers no files
matching the chosen platforms/languages; `pending` – wanted files not yet downloaded;
`downloading` – at least one file currently transferring; `partial` – some done, some
pending, none active; `error` – at least one file failed; `complete` – every wanted file done.

### `GET /api/games/{id}`

```json
{
  "game": { ...GameSummary... },
  "products": [ {
    "id": 1207658924, "title": "The Witcher", "is_dlc": false,
    "files": [ {
      "id": 42,
      "kind": "installer",             // "installer" | "extra"
      "os": "windows",                 // "windows" | "mac" | "linux" | "" for extras
      "language": "en",
      "name": "The Witcher",           // GOG's label for the installer/extra
      "version": "1.5",
      "size": 1234,
      "filename": "setup_the_witcher_1.5.exe", // null until the download link was resolved
      "status": "done",                // "pending" | "downloading" | "done" | "error" | "inactive"
      "progress": { "downloaded_bytes": 0, "speed_bps": 0 },   // only while downloading
      "error": null,
      "local_path": "/library/The Witcher/windows/setup_the_witcher_1.5.exe",
      "md5": null,
      "downloaded_at": null
    } ]
  } ]
}
```

### `POST /api/games/{id}/sync` – refresh this game's details from GOG now. `{ "ok": true }`.
### `POST /api/games/{id}/retry` – reset this game's failed files to pending. `{ "ok": true }`.
### `POST /api/files/{id}/retry` – reset one failed file to pending. `{ "ok": true }`.

## Sync and downloads

### `POST /api/sync` – start a full library sync. 202 `{ "ok": true }` or 409 if already running.

### `GET /api/downloads`

```json
{
  "paused": false,
  "active": [ { "file_id": 42, "game_id": 1, "game_title": "The Witcher", "filename": "setup.exe",
                "size": 1000, "downloaded_bytes": 500, "speed_bps": 1000000, "eta_seconds": 10 } ],
  "queued_total": 30,
  "queue": [ { "file_id": 43, "game_id": 1, "game_title": "...", "name": "...", "os": "windows", "size": 1000 } ]
}
```

### `POST /api/downloads/pause` / `POST /api/downloads/resume` – `{ "paused": true|false }`.

## Logs

### `GET /api/logs?level=&q=&before_id=&limit=100`
`level`: minimum level, one of `debug` | `info` | `warn` | `error`.

```json
{ "logs": [ { "id": 10, "ts": "...", "level": "info", "component": "sync", "message": "..." } ],
  "has_more": true }
```
Results are newest first; pass the smallest `id` you have as `before_id` to page backwards.
