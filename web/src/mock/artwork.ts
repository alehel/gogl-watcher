// DESIGN MOCK. What the artwork GOG publishes for a game would look like in
// the UI once artwork downloads exist. Nothing here talks to the backend: the
// list is made up per game, and the images are drawn from the title in the
// same style as the mock library's covers, so the set reads as one game.
//
// The real thing would come from GOG's public game endpoints (the same ones
// the app already reads the portrait cover and the store tile from) and be
// tracked like any other file of the game.
import type { FileStatus, GameSummary, OfferItem } from "../api";

export type ArtworkKind = "cover" | "background" | "galaxy_background" | "logo" | "icon" | "screenshot";

export interface ArtworkItem {
  id: string;
  kind: ArtworkKind;
  label: string;
  /** Path under the game's folder. */
  path: string;
  width: number;
  height: number;
  format: "JPEG" | "PNG";
  size: number;
  status: FileStatus;
  /** Fraction downloaded, for a file that is downloading. */
  progress?: number;
  downloaded_at: string | null;
  preview: string;
  /** Where GOG publishes it. */
  source: string;
}

/** The same FNV-1a hue the mock library colours its covers with. */
function hueOf(title: string): number {
  let h = 2166136261;
  for (let i = 0; i < title.length; i++) {
    h = Math.imul(h ^ title.charCodeAt(i), 16777619) >>> 0;
  }
  return h % 360;
}

const esc = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&apos;");
const svg = (w: number, h: number, body: string) =>
  "data:image/svg+xml;charset=utf-8," +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}">${body}</svg>`,
  );
const FONT = 'font-family="Helvetica,Arial,sans-serif"';

function cover(title: string, h: number): string {
  const lines = wrap(title, 16).slice(0, 4);
  let y = 300 - (lines.length - 1) * 17;
  const text = lines
    .map((l) => {
      const t = `<text x="24" y="${y}" ${FONT} font-size="26" font-weight="700" fill="#fff">${esc(l)}</text>`;
      y += 34;
      return t;
    })
    .join("");
  return svg(
    300,
    450,
    `<rect width="300" height="450" fill="hsl(${h},35%,22%)"/><circle cx="230" cy="110" r="140" fill="hsl(${(h + 40) % 360},45%,32%)"/>` +
      `<rect y="250" width="300" height="200" fill="rgba(0,0,0,0.35)"/>${text}`,
  );
}

function background(title: string, h: number, galaxy: boolean): string {
  const h2 = (h + 40) % 360;
  const title_ = galaxy
    ? ""
    : `<text x="96" y="980" ${FONT} font-size="64" font-weight="700" fill="#fff" opacity="0.9">${esc(title)}</text>`;
  return svg(
    1920,
    1080,
    `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(${h},40%,18%)"/><stop offset="1" stop-color="hsl(${h2},45%,30%)"/></linearGradient></defs>` +
      `<rect width="1920" height="1080" fill="url(#g)"/>` +
      `<circle cx="${galaxy ? 1200 : 1450}" cy="${galaxy ? 380 : 300}" r="420" fill="hsl(${h2},50%,38%)" opacity="0.8"/>` +
      `<path d="M0 820 Q 480 640 960 800 T 1920 760 V1080 H0Z" fill="hsl(${h},35%,12%)"/>${title_}`,
  );
}

function logo(title: string, h: number): string {
  const lines = wrap(title, 18).slice(0, 2);
  const size = lines.length === 1 ? 120 : 100;
  const text = lines
    .map(
      (l, i) =>
        `<text x="500" y="${lines.length === 1 ? 240 : 170 + i * 130}" text-anchor="middle" ${FONT} font-size="${size}" font-weight="800" ` +
        `fill="hsl(${h},70%,88%)" stroke="hsl(${h},40%,20%)" stroke-width="8" paint-order="stroke" stroke-linejoin="round">${esc(l)}</text>`,
    )
    .join("");
  return svg(1000, 400, text);
}

function icon(title: string, h: number): string {
  const initials = title
    .split(/[\s:]+/)
    .filter((w) => /^[a-z0-9]/i.test(w))
    .slice(0, 2)
    .map((w) => w[0].toUpperCase())
    .join("");
  return svg(
    512,
    512,
    `<defs><clipPath id="c"><rect width="512" height="512" rx="96"/></clipPath></defs>` +
      `<g clip-path="url(#c)"><rect width="512" height="512" fill="hsl(${h},35%,22%)"/><circle cx="380" cy="150" r="200" fill="hsl(${(h + 40) % 360},45%,32%)"/></g>` +
      `<text x="256" y="335" text-anchor="middle" ${FONT} font-size="220" font-weight="700" fill="#fff">${esc(initials)}</text>`,
  );
}

function screenshot(h: number, i: number): string {
  const hh = (h + i * 25) % 360;
  const blocks = Array.from({ length: 7 }, (_, k) => {
    const x = ((k * 331 + i * 97) % 1700) + 60;
    const w = 120 + ((k * 53 + i * 31) % 220);
    const hgt = 160 + ((k * 89 + i * 17) % 360);
    return `<rect x="${x}" y="${800 - hgt}" width="${w}" height="${hgt}" fill="hsl(${(hh + k * 12) % 360},30%,${22 + k * 4}%)"/>`;
  }).join("");
  return svg(
    1920,
    1080,
    `<defs><linearGradient id="s" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="hsl(${hh},45%,40%)"/><stop offset="1" stop-color="hsl(${hh},40%,70%)"/></linearGradient></defs>` +
      `<rect width="1920" height="1080" fill="url(#s)"/>${blocks}<rect y="800" width="1920" height="280" fill="hsl(${hh},30%,18%)"/>` +
      `<rect x="40" y="40" width="360" height="28" rx="4" fill="rgba(0,0,0,0.45)"/><rect x="44" y="44" width="${120 + i * 50}" height="20" rx="3" fill="hsl(${(hh + 120) % 360},70%,55%)"/>`,
  );
}

function wrap(title: string, max: number): string[] {
  const lines: string[] = [];
  let cur = "";
  for (const w of title.split(/\s+/).filter(Boolean)) {
    if (cur && cur.length + 1 + w.length > max) {
      lines.push(cur);
      cur = w;
    } else {
      cur = cur ? `${cur} ${w}` : w;
    }
  }
  if (cur) lines.push(cur);
  return lines;
}

/**
 * The artwork of a game as the UI would list it. A complete game has all of
 * it; a game still downloading is caught mid-way, so both states can be seen.
 */
export function mockArtwork(game: Pick<GameSummary, "title" | "status">, now = Date.now()): ArtworkItem[] {
  const h = hueOf(game.title);
  const complete = game.status === "complete" || game.status === "unavailable";
  const ago = (hours: number) => new Date(now - hours * 3600e3).toISOString();
  const kb = 1024;
  const item = (
    kind: ArtworkKind,
    label: string,
    path: string,
    [width, height]: [number, number],
    format: "JPEG" | "PNG",
    size: number,
    preview: string,
    source: string,
    pending?: FileStatus,
  ): ArtworkItem => {
    const status: FileStatus = complete || !pending ? "done" : pending;
    return {
      id: path,
      kind,
      label,
      path,
      width,
      height,
      format,
      size,
      status,
      progress: status === "downloading" ? 0.42 : undefined,
      downloaded_at: status === "done" ? ago(26 + (h % 20)) : null,
      preview,
      source,
    };
  };
  const shots = 3 + (h % 3);
  return [
    item(
      "cover",
      "Cover",
      "artwork/cover.jpg",
      [1200, 1600],
      "JPEG",
      612 * kb,
      cover(game.title, h),
      "v2 games · boxArtImage",
    ),
    item(
      "background",
      "Background",
      "artwork/background.jpg",
      [1920, 1080],
      "JPEG",
      1640 * kb,
      background(game.title, h, false),
      "v2 games · backgroundImage",
    ),
    item(
      "galaxy_background",
      "Background (Galaxy)",
      "artwork/background_galaxy.jpg",
      [1920, 1080],
      "JPEG",
      1210 * kb,
      background(game.title, h, true),
      "v2 games · galaxyBackgroundImage",
    ),
    item("logo", "Logo", "artwork/logo.png", [1000, 400], "PNG", 96 * kb, logo(game.title, h), "v2 games · logo"),
    item(
      "icon",
      "Icon",
      "artwork/icon.png",
      [512, 512],
      "PNG",
      48 * kb,
      icon(game.title, h),
      "v2 games · iconSquare",
      "downloading",
    ),
    ...Array.from({ length: shots }, (_, i) =>
      item(
        "screenshot",
        `Screenshot ${i + 1}`,
        `artwork/screenshots/${String(i + 1).padStart(2, "0")}.jpg`,
        [1920, 1080],
        "JPEG",
        (640 + ((h + i * 37) % 400)) * kb,
        screenshot(h, i),
        "products · screenshots",
        i < 2 ? undefined : "pending",
      ),
    ),
  ];
}

/** The same artwork as rows of a game's offer: one per image, the screenshots as one item. */
export function mockArtworkOffer(game: Pick<GameSummary, "title" | "status">): OfferItem[] {
  const items = mockArtwork(game);
  const row = (name: string, type: string, size: number, files: number): OfferItem => ({
    kind: "artwork",
    os: "",
    language: "",
    name,
    version: "",
    type,
    size,
    files,
    wanted: true,
  });
  const shots = items.filter((i) => i.kind === "screenshot");
  return [
    ...items
      .filter((i) => i.kind !== "screenshot")
      .map((i) => row(i.label, i.kind === "galaxy_background" ? "background" : i.kind, i.size, 1)),
    row(
      "Screenshots",
      "screenshots",
      shots.reduce((n, s) => n + s.size, 0),
      shots.length,
    ),
  ];
}
