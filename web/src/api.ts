// Typed client for the gogl-watcher HTTP API (see docs/api.md).
// All requests are relative to the current origin; the backend serves the SPA.

export type SetupStep = "auth" | "games" | "platforms" | "content" | "done";
/** Which games are downloaded: everything owned, or only games selected in the library. Empty until setup asked. */
export type DownloadMode = "" | "all" | "selected";
export type SyncPhase = "" | "listing" | "details" | "reconciling";
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
export type FileKind = "installer" | "extra";
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
  bytes_total: number;
  bytes_done: number;
}

export interface DiskState {
  library_dir: string;
  free_bytes: number;
  total_bytes: number;
}

/** How much of the library the size catalog covers, and what the scan is doing. */
export interface CatalogState {
  games_scanned: number;
  games_total: number;
  scanning: boolean;
  last_error: string | null;
  next_scan_at: string | null;
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
  catalog: CatalogState;
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
  include_dlc: boolean;
  include_extras: boolean;
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

/**
 * What a backup of the whole library would need under a combination of
 * settings, whichever games are selected for download. Sizes come from GOG's
 * manifests, so they are close to but not exactly the bytes that arrive, and
 * they only cover the games scanned so far.
 */
export interface SettingsEstimate {
  files: number;
  bytes: number;
  games_scanned: number;
  games_total: number;
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
  owned: boolean;
  /** Chosen for download; only meaningful when download_mode is "selected". */
  selected: boolean;
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
export const postAuthCode = (code: string) =>
  request<{ user: User }>("POST", "/api/auth/code", { code });
export const getAuth = () => request<AuthInfo>("GET", "/api/auth");
export const logout = () => request<void>("POST", "/api/auth/logout");

// ---- Setup ----
export const completeSetup = () => request<{ ok: true }>("POST", "/api/setup/complete");

// ---- Settings ----
export const getSettings = () => request<Settings>("GET", "/api/settings");
export const previewSettings = (settings: Settings) =>
  request<SettingsPreview>("POST", "/api/settings/preview", settings);
/** Estimates for the settings in force. */
export const getEstimate = () => request<SettingsEstimate>("GET", "/api/settings/estimate");
/** Estimates for settings that have not been applied, without storing anything. */
export const estimateSettings = (settings: Settings) =>
  request<SettingsEstimate>("POST", "/api/settings/estimate", settings);
export const putSettings = (settings: Settings, onRemoved: OnRemoved = null) =>
  request<Settings>("PUT", "/api/settings", { settings, on_removed: onRemoved });
export const getLanguages = () =>
  request<{ languages: LanguageOption[] }>("GET", "/api/settings/languages");

// ---- Library ----
export interface GamesQuery {
  q?: string;
  status?: GameStatus | "";
  sort?: GameSort;
  /** Lists games with files on disk before the rest; `sort` orders each group. */
  downloaded_first?: boolean;
}
export const getGames = (query: GamesQuery = {}) =>
  request<{ games: GameSummary[] }>("GET", `/api/games${qs(query)}`);
export const getGame = (id: number | string) => request<GameDetail>("GET", `/api/games/${id}`);
export const syncGame = (id: number | string) =>
  request<{ ok: true }>("POST", `/api/games/${id}/sync`);
export const retryGame = (id: number | string) =>
  request<{ ok: true }>("POST", `/api/games/${id}/retry`);
export const retryFile = (id: number | string) =>
  request<{ ok: true }>("POST", `/api/files/${id}/retry`);
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

/** Extracts a human readable message from any thrown value. */
export function errorMessage(e: unknown): string {
  if (e instanceof NetworkError) return "API unreachable";
  if (e instanceof Error) return e.message;
  return String(e);
}
