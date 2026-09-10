import { useState } from "react";
import { Link } from "react-router-dom";
import { getGames, type GameSort, type GameStatus, type GameSummary } from "../api";
import { ApiErrorNotice, EmptyState, Loading, TimeAgo } from "../components/Common";
import { DataTable } from "../components/DataTable";
import { ProgressBar } from "../components/ProgressBar";
import { useGameSelection } from "../components/Selection";
import { GameStatusDot, gameStatusLabel, gameStatusTone } from "../components/StatusDot";
import { useStatus } from "../components/StatusContext";
import { formatBytes, platformsText, plural } from "../format";
import { useDebounced, usePolling } from "../hooks";

const STATUSES: GameStatus[] = [
  "complete",
  "downloading",
  "pending",
  "partial",
  "error",
  "unavailable",
  "unsynced",
  "unselected",
];
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
  return g.status !== "complete" && g.status !== "unavailable" && g.status !== "unsynced" && g.status !== "unselected";
}

/** Checkbox that flips immediately and snaps back to the server state once it is refreshed. */
function SelectBox({
  checked,
  disabled,
  onChange,
  label,
}: {
  checked: boolean;
  disabled: boolean;
  onChange: (v: boolean) => void;
  label: string;
}) {
  const [local, setLocal] = useState(checked);
  const [lastChecked, setLastChecked] = useState(checked);
  if (checked !== lastChecked) {
    setLastChecked(checked);
    setLocal(checked);
  }
  return (
    <input
      type="checkbox"
      checked={local}
      disabled={disabled}
      onChange={(e) => {
        setLocal(e.target.checked);
        onChange(e.target.checked);
      }}
      aria-label={label}
    />
  );
}

export function LibraryPage() {
  const { status, refresh: refreshStatus } = useStatus();
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
  const selection = useGameSelection(() => {
    games.refresh();
    refreshStatus();
  });

  const totals = status?.library;
  const selectedOnly = totals?.download_mode === "selected";
  const counts: Partial<Record<GameStatus, number>> = totals
    ? {
        complete: totals.complete,
        downloading: totals.downloading,
        pending: totals.pending,
        partial: totals.partial,
        error: totals.error,
        unavailable: totals.unavailable,
        unsynced: totals.unsynced,
        unselected: totals.unselected,
      }
    : {};
  const withCount = (label: string, n: number | undefined) => (n === undefined ? label : `${label} (${n})`);
  const visibleStatuses = selectedOnly ? STATUSES : STATUSES.filter((s) => s !== "unselected");

  const list = games.data?.games ?? [];
  const nothingSelected = selectedOnly && !!totals && totals.games > 0 && totals.unselected === totals.games;

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
          {visibleStatuses.map((s) => (
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
      {nothingSelected && (
        <div className="strip info">
          Only selected games are downloaded, and none are selected yet. Tick the games you want and their installers
          will be fetched.
        </div>
      )}
      {selection.modal}

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
            <GridItem
              key={g.id}
              game={g}
              selectable={selectedOnly}
              busy={selection.busy}
              onSelect={(on) => void selection.setSelection([g.id], on)}
            />
          ))}
        </div>
      ) : (
        <GamesTable
          games={list}
          selectable={selectedOnly}
          busy={selection.busy}
          onSelect={(ids, on) => void selection.setSelection(ids, on)}
        />
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

function GamesTable({
  games,
  selectable,
  busy,
  onSelect,
}: {
  games: GameSummary[];
  selectable: boolean;
  busy: boolean;
  onSelect: (ids: number[], selected: boolean) => void;
}) {
  const selectedCount = games.filter((g) => g.selected).length;
  const allSelected = games.length > 0 && selectedCount === games.length;
  const toggleAll = () => {
    const target = !allSelected;
    const ids = games.filter((g) => g.selected !== target).map((g) => g.id);
    onSelect(ids, target);
  };
  return (
    <DataTable>
      <thead>
        <tr>
          {selectable && (
            <th className="select-cell">
              <input
                type="checkbox"
                checked={allSelected}
                ref={(el) => {
                  if (el) el.indeterminate = selectedCount > 0 && !allSelected;
                }}
                disabled={busy || games.length === 0}
                onChange={toggleAll}
                aria-label={allSelected ? "Deselect all listed games" : "Select all listed games"}
                title={
                  allSelected
                    ? `Deselect ${plural(games.length, "listed game")}`
                    : `Select ${plural(games.length, "listed game")}`
                }
              />
            </th>
          )}
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
          <tr key={g.id} className={selectable && !g.selected ? "dim" : ""}>
            {selectable && (
              <td className="select-cell">
                <SelectBox
                  checked={g.selected}
                  disabled={busy}
                  onChange={(on) => onSelect([g.id], on)}
                  label={`Download ${g.title}`}
                />
              </td>
            )}
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

function GridItem({
  game,
  selectable,
  busy,
  onSelect,
}: {
  game: GameSummary;
  selectable: boolean;
  busy: boolean;
  onSelect: (selected: boolean) => void;
}) {
  const [broken, setBroken] = useState(false);
  const show = !!game.image && !broken;
  return (
    <div className={`grid-cell${selectable && !game.selected ? " dim" : ""}`}>
      <Link to={`/library/${game.id}`} className="grid-item">
        <div className="cover">
          {show && <img src={game.image ?? undefined} alt="" loading="lazy" onError={() => setBroken(true)} />}
          {inProgress(game) && <ProgressBar line value={game.progress} tone={gameStatusTone(game.status)} />}
        </div>
        <div className="title" title={game.title}>
          {game.title}
        </div>
      </Link>
      {selectable ? (
        <label className="check grid-select">
          <SelectBox checked={game.selected} disabled={busy} onChange={onSelect} label={`Download ${game.title}`} />
          <GameStatusDot status={game.status} />
        </label>
      ) : (
        <GameStatusDot status={game.status} />
      )}
    </div>
  );
}
