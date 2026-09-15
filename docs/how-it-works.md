# How it works

- **Sync** (`internal/library`): fetches the owned product ids and the account game list, then
  each game's download manifest from `api.gog.com` (in "selected games only" mode, only for
  selected games; the rest are just listed). For every game it plans the wanted files
  from your settings (base game installers, platforms, languages with optional fallback, DLC,
  patches, extras) and reconciles them with the database: new files become *pending*, files that
  really changed become *pending* again (the old file is deleted after the new one succeeds),
  files that are no longer offered are marked *inactive*. Installers of different languages
  often share their file names, so every installer goes into its language's folder
  (`<Game>/<os>/<language>/`), and a download never writes over a file that belongs to
  another tracked one.
- **Patches** (`internal/library/plan.go`): GOG publishes patches (the update from one
  version of an offline installer to the next) in the same manifest as the installers, per
  platform and language, and describes them the same way. With patches switched on (for the
  library, or for one game on its page) a platform's patches come in the languages its
  installer is picked in, whether or not the installers themselves are downloaded: the
  languages and their fallback are decided once, for the installer, and a patch is only of
  use for an installer of the same language. (Where GOG lists no installer for a platform,
  the patches are picked like installers.) They are stored under the installer's language
  folder in `patches/` (`<Game>/<os>/<language>/patches/`, or
  `<Game>/dlc/<DLC>/<os>/<language>/patches/`), next to the installer they update. They are
  tracked like installers: a patch GOG replaces
  under the same id is fetched again and the old copy removed once the new one is verified,
  and a patch GOG withdraws is marked *inactive* and left on disk, so an installed game can
  still be updated with it.
- **Deciding that a file changed** (`internal/library/sync.go`): GOG publishes no build
  identity for offline installers, so three signals are combined, cheapest first. The
  installer's version string and manifest size come with the manifest and cost nothing, but
  they move when nothing changed and stay put when something did. The newest *Galaxy build id*
  of the game (one request per platform, at most every six hours) is a hint that a game was
  rebuilt; Galaxy's chunked builds are not the offline installers and the two are published
  separately, so it only marks a game as worth a closer look. **GOG's published MD5** for the
  file settles it: it is fetched for the files the first two signals point at, and for a slow
  rolling re-check of everything else (25 files per sync, each file at least monthly), so a
  silent replacement is found even for the Linux builds and extras Galaxy never covers. A copy
  whose checksum still matches is kept, however much its version string moved, which is what
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
- **Server** (`internal/server`): JSON API under `/api` (documented in [`api.md`](api.md))
  and the embedded SPA from `web/`.
