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
  "setup_step": "auth",            // "auth" | "games" | "platforms" | "content" | "done"
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
    "partial": 0, "error": 0, "unavailable": 0, "unsynced": 0, "unselected": 0,
    "download_mode": "all",         // "all" | "selected" | "selected_new" (see settings)
    "include_installers": true, "include_dlc": true, "include_patches": false, "include_extras": false, "include_saves": false,  // the library-wide settings; a game can opt in on its own
    "bytes_total": 0, "bytes_done": 0
  },
  "disk": { "library_dir": "/library", "free_bytes": 0, "total_bytes": 0 }
}
```

`setup_step` tells the UI which wizard step is next: `auth` (no valid GOG token yet),
`games` (not yet chosen between downloading every game or only selected ones),
`platforms` (no platform chosen yet, while installers, DLC or patches are included), `content` (the content
choice not made yet),
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
Marks setup as complete. Requires authentication, a download mode, at least one platform (while
installers, DLC or patches are included),
and an explicit content choice (see settings). 200 `{ "ok": true }` or 409 `{ "error": "..." }`.
Triggers the first library sync.

## Settings

### `GET /api/settings`

```json
{
  "download_mode": "all",                // "all" | "selected" | "selected_new"; empty until the setup wizard asked.
                                         // "selected_new" is "selected" plus: a game that appears in the
                                         // library after its first listing is selected as it does.
  "platforms": ["windows"],              // any of "windows" | "mac" | "linux"; empty until chosen
  "languages": ["en"],                   // GOG language codes, e.g. "en", "de", "fr"
  "language_fallback": true,             // download another language if none of the chosen exist
  "content_chosen": false,               // becomes true once the setup wizard stored the choice
  "include_installers": true,            // the offline installers of the base games themselves
  "include_dlc": true,
  "include_patches": false,              // GOG's patches from one installer version to the next, for the chosen platforms, in the installers' languages
  "include_extras": false,
  "include_saves": false,                // back up the cloud saves of games that have any
  "max_concurrent_downloads": 2,         // 1..8
  "speed_limit_kbps": 0,                 // kilobytes per second, 0 = unlimited
  "check_interval_hours": 6,             // 1..168
  "downloads_paused": false
}
```

`download_mode` decides which games are downloaded: `all` keeps every owned game, `selected`
only the games flagged via `PUT /api/games/selection` (none are selected by default). Once
setup is complete the mode can no longer be empty.

### `POST /api/settings/preview`
Body: the full settings object. Returns which already-tracked files would stop being
wanted if these settings were applied:

```json
{
  "needs_confirmation": true,
  "removed": { "files": 12, "bytes": 123456789, "downloaded_files": 10, "downloaded_bytes": 100000000 },
  "reasons": ["platform:linux", "installers", "patches", "extras", "dlc", "saves", "language:de", "unselected"]
}
```

`unselected` appears when switching to `download_mode: "selected"` (or `"selected_new"`) would
drop files of games that are not selected.

### `PUT /api/settings`
Body `{ "settings": {...}, "on_removed": "keep" | "delete" | null }`.
If the change drops previously-wanted files and `on_removed` is null → 409
`{ "error": "confirmation_required", ... same fields as preview ... }`.
With `"keep"` the files stay on disk and are shown as *inactive*; with `"delete"` they are
removed from disk and from the game's file list. 200 returns the stored settings.

Language codes available for the picker: `GET /api/settings/languages` →
`{ "languages": [ { "code": "en", "name": "English" }, ... ] }`.

## Library

### `GET /api/games?q=&status=&tag=&sort=&downloaded_first=`
`status` filter: one of the game statuses below; `tag`: only games carrying this gog.com tag;
`sort`: `title` (default) | `status` | `updated`.
`downloaded_first=1` lists the games that already have files on disk (`files_done > 0`) before
the rest; `sort` then orders each of the two groups. `tags` lists the user's own gog.com tags
that are in use on an owned game, with counts, whatever the filter.

```json
{ "tags": [ { "name": "Favorite", "count": 12 } ],
  "games": [ {
  "id": 1207658924,
  "title": "The Witcher",
  "slug": "the_witcher",
  "image": "https://images.gog-statics.com/...jpg",   // portrait box art, or GOG's store tile when it has none
  "folder": "The Witcher",
  "works_on": { "windows": true, "mac": true, "linux": false },
  "tags": ["Completed", "Favorite"],   // the user's own gog.com tags, sorted
  "owned": true,
  "selected": false,      // flagged for download; only matters when download_mode is "selected"
  "include_installers": false, "include_dlc": false, "include_patches": false, "include_extras": true, "include_saves": false,  // the game's own opt-ins; matter when the library-wide setting is off
  "status": "complete",   // "complete" | "downloading" | "pending" | "partial" | "error" | "unavailable" | "unsynced" | "unselected"
  "files_total": 3, "files_done": 3,
  "bytes_total": 1234, "bytes_done": 1234,
  "progress": 1.0,
  "last_synced_at": "...",  // when the game's details were last fetched from GOG
  "updated_at": "..."       // when something last changed about the game: a sync found new, updated or
                            // dropped files, its listing changed, or it was selected or opted in; what
                            // `sort=updated` orders by
} ] }
```

Status meaning: `unselected` – download mode is `selected` or `selected_new` and the game is not selected, so
nothing is fetched for it; `unsynced` – details not fetched yet; `unavailable` – GOG offers no files
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
      "kind": "installer",             // "installer" | "patch" | "extra" | "save"
      "os": "windows",                 // "windows" | "mac" | "linux" | "" for extras and saves
      "language": "en",
      "name": "The Witcher",           // GOG's label for the installer/patch/extra; a cloud save's path in the cloud
      "version": "1.5",
      "size": 1234,
      "filename": "setup_the_witcher_1.5.exe", // null until the download link was resolved
      "status": "done",                // "pending" | "downloading" | "done" | "error" | "inactive"
      "progress": { "downloaded_bytes": 0, "speed_bps": 0 },   // only while downloading
      "error": null,
      "local_path": "/library/The Witcher/windows/en/setup_the_witcher_1.5.exe",  // a patch: .../windows/en/patches/patch_the_witcher_1.4_to_1.5.exe
      "md5": null,
      "downloaded_at": null
    } ]
  } ]
}
```

### `PUT /api/games/selection`
Body `{ "ids": [1207658924, 1207664663], "selected": true, "on_removed": "keep" | "delete" | null }`.
Flags games for download (or removes the flag). Selecting fetches the games' details in the
background and queues their files; deselecting drops their tracked files exactly like a settings
change does: with downloaded files present and `on_removed` null → 409
`{ "error": "confirmation_required", "removed": {...}, "reasons": ["unselected"] }`, otherwise
200 `{ "ok": true, "selected": true, "ids": [...] }`. Unknown ids → 404. The flag is stored in
any download mode but only has an effect in `selected` and `selected_new`.

### `PUT /api/games/{id}/options`
Body `{ "include_installers": false, "include_dlc": false, "include_patches": false, "include_extras": true, "include_saves": false, "on_removed": "keep" | "delete" | null }`.
Opts one game in to (or out of) base game installers, DLC, patches, extras and cloud saves regardless of the library-wide settings; the
setting that is on for the library wins either way. Opting in syncs the game right away so the
files get planned; opting out drops the files no longer wanted like a settings change does (409
`confirmation_required` with downloaded files present and `on_removed` null).
200 `{ "ok": true, "include_installers": false, "include_dlc": false, "include_patches": false, "include_extras": true, "include_saves": false }`. Unknown id → 404.

### `GET /api/games/{id}/offer`
What GOG offers for the game right now, without planning any of it: every installer, patch and extra of
the game and its owned DLC, with `wanted` on the ones the current settings (and the game's own
opt-ins) would download. Meant for the page of a game that is not selected. Served from a
ten-minute cache; 502 when GOG cannot be reached.

```json
{ "fetched_at": "...", "wanted_files": 3, "wanted_bytes": 88000000,
  "products": [ { "id": 1207658924, "title": "The Witcher", "is_dlc": false, "items": [
    { "kind": "installer", "os": "windows", "language": "en", "name": "The Witcher", "version": "1.5",
      "size": 88000000, "files": 3, "wanted": true },
    { "kind": "patch", "os": "windows", "language": "en", "name": "Patch 1.4 → 1.5", "version": "1.5",
      "size": 23000000, "files": 2, "wanted": false },
    { "kind": "extra", "os": "", "language": "", "name": "Soundtrack", "type": "soundtrack",
      "version": "", "size": 12000000, "files": 1, "wanted": false }
  ] } ] }
```

### `POST /api/games/{id}/sync` – refresh this game's details from GOG now. `{ "ok": true }`; 502 with
`"interrupted by a settings change"` when a settings change stopped it (the full sync that change
triggers plans the game again).
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
  // eta_seconds is null while the speed (transfer just started or stalled) or the size is unknown
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
