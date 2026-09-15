// Typed client for the gogl-watcher HTTP API (see docs/api.md).
// All requests are relative to the current origin; the backend serves the SPA.

export type SetupStep = "auth" | "games" | "platforms" | "content" | "done";
/** Which games are downloaded: everything owned, or only games selected in the library. Empty until setup asked. */
export type DownloadMode = "" | "all" | "selected" | "selected_new";
/** Whether the mode downloads only selected games (with or without new games selecting themselves). */
export const selectsGames = (mode: DownloadMode | undefined) => mode === "selected" || mode === "selected_new";
export type SyncPhase = "" | "listing" | "artwork" | "details" | "reconciling";
export type Platform = "windows" | "mac" | "linux";
export type GameStatus =
  | "complete"
  | "downloading"
  | "pending"
  | "partial"
  | "error"
  | "unavailable"
  | "unsynced"
  | "unselected";
export type FileStatus = "pending" | "downloading" | "done" | "error" | "inactive";
// "artwork" is a DESIGN MOCK: the backend does not send it yet.
export type FileKind = "installer" | "extra" | "save" | "artwork";
export type LogLevel = "debug" | "info" | "warn" | "error";
export type GameSort = "title" | "status" | "updated";
export type OnRemoved = "keep" | "delete" | null;

export interface User {
  id: string;
  username: string;
}

export interface SyncState {
  running: boolean;
  phase: SyncPhase;
  games_total: number;
  games_done: number;
  last_started_at: string | null;
  last_finished_at: string | null;
  last_error: string | null;
  next_run_at: string | null;
}

export interface DownloadsState {
  paused: boolean;
  active: number;
  queued: number;
  speed_bps: number;
}

export interface LibraryTotals {
  games: number;
  complete: number;
  pending: number;
  downloading: number;
  partial: number;
  error: number;
  unavailable: number;
  unsynced: number;
  unselected: number;
  download_mode: DownloadMode;
  /** The library-wide content settings; a game can opt in on its own when they are off. */
  include_installers: boolean;
  include_dlc: boolean;
  include_extras: boolean;
  include_saves: boolean;
  bytes_total: number;
  bytes_done: number;
}

export interface DiskState {
  library_dir: string;
  free_bytes: number;
  total_bytes: number;
}

export interface Status {
  version: string;
  setup_complete: boolean;
  setup_step: SetupStep;
  authenticated: boolean;
  auth_error: string | null;
  user: User | null;
  sync: SyncState;
  downloads: DownloadsState;
  library: LibraryTotals;
  disk: DiskState;
}

export interface AuthInfo {
  authenticated: boolean;
  user: User | null;
  expires_at: string | null;
  error: string | null;
}

export interface Settings {
  download_mode: DownloadMode;
  platforms: Platform[];
  languages: string[];
  language_fallback: boolean;
  content_chosen: boolean;
  /** The offline installers of the base games themselves. */
  include_installers: boolean;
  include_dlc: boolean;
  include_extras: boolean;
  /** Back up the cloud saves of games that have any. */
  include_saves: boolean;
  max_concurrent_downloads: number;
  speed_limit_kbps: number;
  check_interval_hours: number;
  downloads_paused: boolean;
}

export interface RemovedSummary {
  files: number;
  bytes: number;
  downloaded_files: number;
  downloaded_bytes: number;
}

export interface SettingsPreview {
  needs_confirmation: boolean;
  removed: RemovedSummary;
  reasons: string[];
}

export interface LanguageOption {
  code: string;
  name: string;
}

export interface WorksOn {
  windows: boolean;
  mac: boolean;
  linux: boolean;
}

export interface GameSummary {
  id: number;
  title: string;
  slug: string;
  image: string | null;
  folder: string;
  works_on: WorksOn;
  /** The user's own gog.com tags on the game, sorted. */
  tags: string[];
  owned: boolean;
  /** Chosen for download; only meaningful when the download mode selects games. */
  selected: boolean;
  /** The game's own content opt-ins; they matter when the library-wide setting is off. */
  include_installers: boolean;
  include_dlc: boolean;
  include_extras: boolean;
  include_saves: boolean;
  status: GameStatus;
  files_total: number;
  files_done: number;
  bytes_total: number;
  bytes_done: number;
  progress: number;
  last_synced_at: string | null;
  updated_at: string | null;
}

export interface FileProgress {
  downloaded_bytes: number;
  speed_bps: number;
}

export interface GameFile {
  id: number;
  kind: FileKind;
  os: Platform | "";
  language: string;
  name: string;
  version: string | null;
  size: number;
  filename: string | null;
  status: FileStatus;
  progress?: FileProgress | null;
  error: string | null;
  local_path: string | null;
  md5: string | null;
  downloaded_at: string | null;
}

export interface Product {
  id: number;
  title: string;
  is_dlc: boolean;
  files: GameFile[];
}

export interface GameDetail {
  game: GameSummary;
  products: Product[];
}

/** One installer (possibly in parts) or extra GOG offers for a product. */
export interface OfferItem {
  kind: FileKind;
  os: Platform | "";
  language: string;
  name: string;
  version: string;
  type?: string;
  size: number;
  files: number;
  /** Whether the current settings would download it. */
  wanted: boolean;
}

export interface OfferProduct {
  id: number;
  title: string;
  is_dlc: boolean;
  items: OfferItem[];
}

/** What GOG offers for a game, read live; nothing of it is queued. */
export interface Offer {
  fetched_at: string;
  products: OfferProduct[];
  wanted_files: number;
  wanted_bytes: number;
}

export interface ActiveDownload {
  file_id: number;
  game_id: number;
  game_title: string;
  filename: string;
  size: number;
  downloaded_bytes: number;
  speed_bps: number;
  /** null while the speed is unknown (just started, stalled) or the size is. */
  eta_seconds: number | null;
}

export interface QueuedDownload {
  file_id: number;
  game_id: number;
  game_title: string;
  name: string;
  os: Platform | "";
  size: number;
}

export interface DownloadsResponse {
  paused: boolean;
  active: ActiveDownload[];
  queued_total: number;
  queue: QueuedDownload[];
}

export interface LogEntry {
  id: number;
  ts: string;
  level: LogLevel;
  component: string;
  message: string;
}

export interface LogsResponse {
  logs: LogEntry[];
  has_more: boolean;
}

/** Error thrown for non-2xx responses. `body` holds the parsed JSON when available. */
export class ApiError extends Error {
  status: number;
  body: unknown;
  constructor(status: number, message: string, body: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
  /** True when the backend asked for confirmation before applying settings. */
  get confirmationRequired(): boolean {
    return this.status === 409 && this.message === "confirmation_required";
  }
}

/** Thrown when the request never reached the backend (network down, server restarting). */
export class NetworkError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NetworkError";
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      credentials: "same-origin",
    });
  } catch (e) {
    throw new NetworkError(e instanceof Error ? e.message : "network error");
  }

  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }

  if (!res.ok) {
    // A reverse proxy (or the Vite dev proxy) answering for a dead backend: treat as unreachable
    // so the UI shows "API unreachable" and keeps polling instead of a generic error. The backend
    // itself always answers with a JSON error object (it uses 502 for a failed GOG call), so a
    // 5xx with such a body is a real error with a message worth showing.
    const isJsonError = data !== null && typeof data === "object";
    if (res.status >= 500 && !isJsonError) {
      throw new NetworkError(`backend unavailable (HTTP ${res.status})`);
    }
    let message = `${res.status} ${res.statusText}`;
    if (data && typeof data === "object" && typeof (data as { error?: unknown }).error === "string") {
      message = (data as { error: string }).error;
    } else if (typeof data === "string" && data.trim()) {
      message = data.trim();
    }
    throw new ApiError(res.status, message, data);
  }
  return data as T;
}

function qs(params: object): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params as Record<string, unknown>)) {
    if (v !== undefined && v !== null && v !== "") sp.set(k, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
}

// ---- Status ----
export const getStatus = () => request<Status>("GET", "/api/status");

// ---- Authentication ----
export const getAuthUrl = () => request<{ url: string }>("GET", "/api/auth/url");
export const postAuthCode = (code: string) => request<{ user: User }>("POST", "/api/auth/code", { code });
export const getAuth = () => request<AuthInfo>("GET", "/api/auth");
export const logout = () => request<void>("POST", "/api/auth/logout");

// ---- Setup ----
export const completeSetup = () => request<{ ok: true }>("POST", "/api/setup/complete");

// ---- Settings ----
export const getSettings = () => request<Settings>("GET", "/api/settings");
export const previewSettings = (settings: Settings) =>
  request<SettingsPreview>("POST", "/api/settings/preview", settings);
export const putSettings = (settings: Settings, onRemoved: OnRemoved = null) =>
  request<Settings>("PUT", "/api/settings", { settings, on_removed: onRemoved });
export const getLanguages = () => request<{ languages: LanguageOption[] }>("GET", "/api/settings/languages");

// ---- Library ----
/** One of the user's gog.com tags, with the number of owned games carrying it. */
export interface Tag {
  name: string;
  count: number;
}

export interface GamesQuery {
  q?: string;
  status?: GameStatus | "";
  /** Only games carrying this gog.com tag. */
  tag?: string;
  sort?: GameSort;
  /** Lists games with files on disk before the rest; `sort` orders each group. */
  downloaded_first?: boolean;
}
export const getGames = (query: GamesQuery = {}) =>
  request<{ games: GameSummary[]; tags: Tag[] }>("GET", `/api/games${qs(query)}`);
export const getGame = (id: number | string) => request<GameDetail>("GET", `/api/games/${id}`);
export const getGameOffer = (id: number | string) => request<Offer>("GET", `/api/games/${id}/offer`);
/** A game's own content opt-ins. */
export interface GameOptions {
  include_installers: boolean;
  include_dlc: boolean;
  include_extras: boolean;
  include_saves: boolean;
}

/** Whether these settings download anything that exists per platform, so a platform has to be chosen. */
export const needsPlatforms = (s: Pick<Settings, "include_installers" | "include_dlc">) =>
  s.include_installers || s.include_dlc;

/** Opts a game in to or out of base game installers, DLC, extras and cloud saves on its own. Opting out of downloaded files needs `onRemoved` (409 otherwise). */
export const setGameOptions = (id: number | string, options: GameOptions, onRemoved: OnRemoved = null) =>
  request<{ ok: true }>("PUT", `/api/games/${id}/options`, { ...options, on_removed: onRemoved });
export const syncGame = (id: number | string) => request<{ ok: true }>("POST", `/api/games/${id}/sync`);
export const retryGame = (id: number | string) => request<{ ok: true }>("POST", `/api/games/${id}/retry`);
export const retryFile = (id: number | string) => request<{ ok: true }>("POST", `/api/files/${id}/retry`);
/** Selects or deselects games for download. Deselecting downloaded games needs `onRemoved` (409 otherwise). */
export const setGameSelection = (ids: number[], selected: boolean, onRemoved: OnRemoved = null) =>
  request<{ ok: true; selected: boolean; ids: number[] }>("PUT", "/api/games/selection", {
    ids,
    selected,
    on_removed: onRemoved,
  });

// ---- Sync and downloads ----
export const startSync = () => request<{ ok: true }>("POST", "/api/sync");
export const getDownloads = () => request<DownloadsResponse>("GET", "/api/downloads");
export const pauseDownloads = () => request<{ paused: boolean }>("POST", "/api/downloads/pause");
export const resumeDownloads = () => request<{ paused: boolean }>("POST", "/api/downloads/resume");

// ---- Logs ----
export interface LogsQuery {
  level?: LogLevel | "";
  q?: string;
  before_id?: number;
  limit?: number;
}
export const getLogs = (query: LogsQuery = {}) =>
  request<LogsResponse>("GET", `/api/logs${qs({ limit: 100, ...query })}`);

/**
 * The removal preview a 409 `confirmation_required` carries, or null for any
 * other failure. Missing parts are filled in so the dialog can always be shown.
 */
export function previewFromError(e: unknown): SettingsPreview | null {
  if (!(e instanceof ApiError) || !e.confirmationRequired) return null;
  const body = e.body as Partial<SettingsPreview>;
  return {
    needs_confirmation: true,
    removed: body.removed ?? { files: 0, bytes: 0, downloaded_files: 0, downloaded_bytes: 0 },
    reasons: body.reasons ?? [],
  };
}

/** Extracts a human readable message from any thrown value. */
export function errorMessage(e: unknown): string {
  if (e instanceof NetworkError) return "API unreachable";
  if (e instanceof Error) return e.message;
  return String(e);
}
