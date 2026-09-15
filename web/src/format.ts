import type { WorksOn } from "./api";

const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

/** Human readable size, binary units (1 KB = 1024 B). */
export function formatBytes(bytes: number | null | undefined, digits = 1): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) return "—";
  if (bytes < 0) bytes = 0;
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  const d = i === 0 ? 0 : digits;
  return `${v.toFixed(d)} ${UNITS[i]}`;
}

/** Bytes per second as MB/s (or KB/s when small). */
export function formatSpeed(bps: number | null | undefined): string {
  if (bps === null || bps === undefined || !Number.isFinite(bps)) return "—";
  if (bps <= 0) return "0 MB/s";
  const mb = bps / (1024 * 1024);
  if (mb >= 0.1) return `${mb.toFixed(mb >= 10 ? 0 : 1)} MB/s`;
  return `${(bps / 1024).toFixed(0)} KB/s`;
}

export function formatPercent(fraction: number | null | undefined): string {
  if (fraction === null || fraction === undefined || !Number.isFinite(fraction)) return "0%";
  const f = Math.max(0, Math.min(1, fraction));
  // Something that is not finished never reads as 100%: floor, do not round, near the end.
  if (f >= 0.995 && f < 1) return `${(Math.floor(f * 1000) / 10).toFixed(1)}%`;
  return `${Math.round(f * 100)}%`;
}

export function ratio(done: number, total: number): number {
  if (!total || total <= 0) return 0;
  return Math.max(0, Math.min(1, done / total));
}

/** Duration in seconds as "1h 02m", "3m 12s", "45s". */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds < 0) return "—";
  const s = Math.round(seconds);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${String(m % 60).padStart(2, "0")}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

export function parseDate(iso: string | null | undefined): Date | null {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** "3 min ago", "in 2 h", "just now". `now` is injectable for re-rendering. */
export function formatRelative(iso: string | null | undefined, now: number = Date.now()): string {
  const d = parseDate(iso);
  if (!d) return "never";
  const diff = (d.getTime() - now) / 1000; // positive = future
  const abs = Math.abs(diff);
  const future = diff > 0;
  let text: string;
  if (abs < 10) return "just now";
  // Round first, then pick the unit, so 59.7 min reads "1 h" rather than "60 min".
  const minutes = Math.round(abs / 60);
  const hours = abs / 3600;
  const days = Math.round(abs / 86400);
  const months = Math.round(abs / (86400 * 30));
  if (abs < 59.5) text = `${Math.round(abs)} s`;
  else if (minutes < 60) text = `${minutes} min`;
  else if (hours < 9.95) text = `${hours.toFixed(1).replace(/\.0$/, "")} h`;
  else if (Math.round(hours) < 24) text = `${Math.round(hours)} h`;
  else if (days < 30) text = `${days} d`;
  else if (months < 12) text = `${months} mo`;
  else text = `${Math.round(abs / (86400 * 365))} y`;
  return future ? `in ${text}` : `${text} ago`;
}

export function formatAbsolute(iso: string | null | undefined): string {
  const d = parseDate(iso);
  if (!d) return "";
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

const pad2 = (n: number) => String(n).padStart(2, "0");

/** "2026-09-15", the half of a log stamp that repeats down the page. */
export function formatLogDate(iso: string): string {
  const d = parseDate(iso);
  if (!d) return iso;
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

/** "11:38:37", the half that tells two log rows apart. */
export function formatLogTimeOfDay(iso: string): string {
  const d = parseDate(iso);
  if (!d) return "";
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
}

export function plural(n: number, singular: string, pluralForm?: string): string {
  return `${n} ${n === 1 ? singular : (pluralForm ?? `${singular}s`)}`;
}

export const PLATFORM_LABELS: Record<string, string> = {
  windows: "Windows",
  mac: "macOS",
  linux: "Linux",
};

export function platformLabel(os: string): string {
  if (!os) return "—";
  return PLATFORM_LABELS[os] ?? os;
}

/** KB/s (as stored by the backend) <-> MB/s (as shown in the UI), binary units. */
export function kbpsToMbps(kbps: number): number {
  return Math.round((kbps / 1024) * 100) / 100;
}
export function mbpsToKbps(mbps: number): number {
  return Math.round(mbps * 1024);
}

const PLATFORM_SHORT: Record<keyof WorksOn, string> = { windows: "Win", mac: "Mac", linux: "Linux" };
const PLATFORM_ORDER: Array<keyof WorksOn> = ["windows", "mac", "linux"];

/** "Win · Mac · Linux" (short) or "Windows · macOS · Linux" (long) from a works_on map. */
export function platformsText(works: WorksOn | null | undefined, long = false): string {
  if (!works) return "";
  return PLATFORM_ORDER.filter((p) => works[p])
    .map((p) => (long ? PLATFORM_LABELS[p] : PLATFORM_SHORT[p]))
    .join(" · ");
}
