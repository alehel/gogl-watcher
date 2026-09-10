import { useState } from "react";
import { Link } from "react-router-dom";
import { getGames, type GameSort, type GameStatus, type GameSummary } from "../api";
import { ApiErrorNotice, EmptyState, Loading } from "../components/Common";
import { IconImage, IconSearch } from "../components/Icons";
import { ProgressBar } from "../components/ProgressBar";
import { GameStatusBadge, gameStatusLabel, gameStatusTone } from "../components/StatusBadge";
import { useStatus } from "../components/StatusContext";
import { useDebounced, usePolling } from "../hooks";

const STATUSES: GameStatus[] = ["complete", "downloading", "pending", "partial", "error", "unavailable", "unsynced"];

export function LibraryPage() {
  const { status } = useStatus();
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<GameStatus | "">("");
  const [sort, setSort] = useState<GameSort>("title");
  const dq = useDebounced(q.trim(), 250);

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

  const list = games.data?.games ?? [];

  return (
    <div>
      <div className="page-head">
        <h1>Library</h1>
        {totals && (
          <span className="muted small">
            {totals.games.toLocaleString()} games
          </span>
        )}
      </div>

      <div className="toolbar">
        <div className="search">
          <IconSearch />
          <input
            className="input"
            type="search"
            placeholder="Search titles…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            aria-label="Search games"
          />
        </div>
        <select className="select" value={sort} onChange={(e) => setSort(e.target.value as GameSort)} aria-label="Sort">
          <option value="title">Sort: Title</option>
          <option value="status">Sort: Status</option>
          <option value="updated">Sort: Recently updated</option>
        </select>
        <div className="chip-list" role="group" aria-label="Filter by status">
          <button className={`chip ${filter === "" ? "on" : ""}`} onClick={() => setFilter("")}>
            All {totals && <span className="count">{totals.games}</span>}
          </button>
          {STATUSES.map((s) => (
            <button
              key={s}
              className={`chip ${filter === s ? "on" : ""}`}
              onClick={() => setFilter(s)}
            >
              <span className="dot" style={{ background: `var(--${gameStatusTone(s)})` }} />
              {gameStatusLabel(s)}
              {counts[s] !== undefined && <span className="count">{counts[s]}</span>}
            </button>
          ))}
        </div>
      </div>

      {games.error ? (
        <div style={{ marginBottom: 12 }}>
          <ApiErrorNotice error={games.error} stale={!!games.data} />
        </div>
      ) : null}

      {games.loading && !games.data ? (
        <Loading text="Loading library…" />
      ) : list.length === 0 ? (
        <EmptyState title={dq || filter ? "No games match" : "No games yet"}>
          {dq || filter
            ? "Try a different search or status filter."
            : "Your GOG library shows up here after the first sync finishes."}
        </EmptyState>
      ) : (
        <div className="lib-grid">
          {list.map((g) => (
            <GameCard key={g.id} game={g} />
          ))}
        </div>
      )}
    </div>
  );
}

function GameCard({ game }: { game: GameSummary }) {
  const [broken, setBroken] = useState(false);
  const showImg = !!game.image && !broken;
  const tone = gameStatusTone(game.status);
  return (
    <Link to={`/library/${game.id}`} className="game-card">
      <div className={`cover ${showImg ? "" : "placeholder"}`}>
        {showImg ? (
          <img src={game.image ?? undefined} alt="" loading="lazy" onError={() => setBroken(true)} />
        ) : (
          <IconImage />
        )}
      </div>
      <div className="body">
        <div className="title" title={game.title}>
          {game.title}
        </div>
        <div className="meta">
          <GameStatusBadge status={game.status} />
          <span className="num">
            {game.files_done} of {game.files_total}
          </span>
        </div>
        {game.status !== "complete" && game.status !== "unavailable" && game.status !== "unsynced" && (
          <ProgressBar value={game.progress} size="sm" tone={tone} striped={game.status === "downloading"} />
        )}
      </div>
    </Link>
  );
}
