import { useState } from "react";
import { Link } from "react-router-dom";
import { getGames, type GameSort, type GameStatus, type GameSummary } from "../api";
import { ApiErrorNotice, EmptyState, Loading, TimeAgo } from "../components/Common";
import { DataTable } from "../components/DataTable";
import { ProgressBar } from "../components/ProgressBar";
import { GameStatusDot, gameStatusLabel, gameStatusTone } from "../components/StatusDot";
import { useStatus } from "../components/StatusContext";
import { formatBytes, platformsText } from "../format";
import { useDebounced, usePolling } from "../hooks";

const STATUSES: GameStatus[] = ["complete", "downloading", "pending", "partial", "error", "unavailable", "unsynced"];
type View = "table" | "grid";
const VIEW_KEY = "gogl-watcher.libraryView";

function readView(): View {
  try {
    return localStorage.getItem(VIEW_KEY) === "grid" ? "grid" : "table";
  } catch {
    return "table";
  }
}

function inProgress(g: GameSummary): boolean {
  return g.status !== "complete" && g.status !== "unavailable" && g.status !== "unsynced";
}

export function LibraryPage() {
  const { status } = useStatus();
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<GameStatus | "">("");
  const [sort, setSort] = useState<GameSort>("title");
  const [view, setViewState] = useState<View>(readView);
  const dq = useDebounced(q.trim(), 250);

  const setView = (v: View) => {
    setViewState(v);
    try {
      localStorage.setItem(VIEW_KEY, v);
    } catch {
      /* ignore */
    }
  };

  const games = usePolling(() => getGames({ q: dq, status: filter, sort }), 5000, [dq, filter, sort]);

  const totals = status?.library;
  const counts: Partial<Record<GameStatus, number>> = totals
    ? {
        complete: totals.complete,
        downloading: totals.downloading,
        pending: totals.pending,
        partial: totals.partial,
        error: totals.error,
        unavailable: totals.unavailable,
        unsynced: Math.max(
          0,
          totals.games -
            (totals.complete + totals.downloading + totals.pending + totals.partial + totals.error + totals.unavailable),
        ),
      }
    : {};
  const withCount = (label: string, n: number | undefined) => (n === undefined ? label : `${label} (${n})`);

  const list = games.data?.games ?? [];

  return (
    <div className="stack">
      <div className="page-head">
        <h1 className="page-title">Library</h1>
        {totals && <span className="muted">{totals.games.toLocaleString()} games</span>}
      </div>

      <div className="toolbar">
        <input
          className="input search"
          type="search"
          placeholder="Search titles"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          aria-label="Search games"
        />
        <select
          className="select"
          value={filter}
          onChange={(e) => setFilter(e.target.value as GameStatus | "")}
          aria-label="Filter by status"
        >
          <option value="">{withCount("All", totals?.games)}</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {withCount(gameStatusLabel(s), counts[s])}
            </option>
          ))}
        </select>
        <select className="select" value={sort} onChange={(e) => setSort(e.target.value as GameSort)} aria-label="Sort">
          <option value="title">Sort: Title</option>
          <option value="status">Sort: Status</option>
          <option value="updated">Sort: Recently updated</option>
        </select>
        <div className="segmented right" role="group" aria-label="View">
          <button type="button" aria-pressed={view === "table"} onClick={() => setView("table")}>
            Table
          </button>
          <button type="button" aria-pressed={view === "grid"} onClick={() => setView("grid")}>
            Grid
          </button>
        </div>
      </div>

      <ApiErrorNotice error={games.error} stale={!!games.data} />

      {games.loading && !games.data ? (
        <Loading text="Loading library…" />
      ) : list.length === 0 ? (
        <EmptyState>
          {dq || filter
            ? "No games match this search or filter."
            : "Your GOG library shows up here after the first sync finishes."}
        </EmptyState>
      ) : view === "grid" ? (
        <div className="lib-grid">
          {list.map((g) => (
            <GridItem key={g.id} game={g} />
          ))}
        </div>
      ) : (
        <GamesTable games={list} />
      )}
    </div>
  );
}

function Thumb({ game, className }: { game: GameSummary; className: string }) {
  const [broken, setBroken] = useState(false);
  const show = !!game.image && !broken;
  return (
    <div className={className}>
      {show && <img src={game.image ?? undefined} alt="" loading="lazy" onError={() => setBroken(true)} />}
    </div>
  );
}

function GamesTable({ games }: { games: GameSummary[] }) {
  return (
    <DataTable>
      <thead>
        <tr>
          <th aria-label="Cover" />
          <th>Title</th>
          <th>Platforms</th>
          <th className="num">Files</th>
          <th className="num">Size</th>
          <th>Status</th>
          <th>Progress</th>
          <th>Synced</th>
        </tr>
      </thead>
      <tbody>
        {games.map((g) => (
          <tr key={g.id}>
            <td className="thumb-cell">
              <Thumb game={g} className="thumb" />
            </td>
            <td className="wrap">
              <Link to={`/library/${g.id}`}>{g.title}</Link>
            </td>
            <td className="faint">{platformsText(g.works_on) || "—"}</td>
            <td className="num">
              {g.files_done} / {g.files_total}
            </td>
            <td className="num">{formatBytes(g.bytes_total)}</td>
            <td>
              <GameStatusDot status={g.status} />
            </td>
            <td>{inProgress(g) ? <ProgressBar value={g.progress} showPercent tone={gameStatusTone(g.status)} /> : null}</td>
            <td className="muted">
              <TimeAgo iso={g.last_synced_at} />
            </td>
          </tr>
        ))}
      </tbody>
    </DataTable>
  );
}

function GridItem({ game }: { game: GameSummary }) {
  const [broken, setBroken] = useState(false);
  const show = !!game.image && !broken;
  return (
    <Link to={`/library/${game.id}`} className="grid-item">
      <div className="cover">
        {show && <img src={game.image ?? undefined} alt="" loading="lazy" onError={() => setBroken(true)} />}
        {inProgress(game) && <ProgressBar line value={game.progress} tone={gameStatusTone(game.status)} />}
      </div>
      <div className="title" title={game.title}>
        {game.title}
      </div>
      <GameStatusDot status={game.status} />
    </Link>
  );
}
