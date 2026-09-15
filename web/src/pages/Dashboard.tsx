import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { getDownloads, NetworkError, selectsGames, startSync, type ActiveDownload, type Status } from "../api";
import { ApiErrorNotice, EmptyState, Loading, Spinner, TimeAgo } from "../components/Common";
import { DataTable } from "../components/DataTable";
import { PauseButton } from "../components/PauseButton";
import { ProgressBar } from "../components/ProgressBar";
import { StatusDot } from "../components/StatusDot";
import { useStatus } from "../components/StatusContext";
import { useToastAction } from "../components/Toast";
import { formatBytes, formatDuration, formatRelative, formatSpeed, plural, ratio } from "../format";
import { useNow, usePolling } from "../hooks";

export function DashboardPage() {
  const { status, error, loading, refresh } = useStatus();
  const downloads = usePolling(getDownloads, 2000);
  const now = useNow();

  if (loading && !status) return <Loading text="Loading status…" />;
  if (!status) return <ApiErrorNotice error={error} />;

  const l = status.library;
  const d = status.downloads;
  const bytesFrac = ratio(l.bytes_done, l.bytes_total);
  const diskFrac = ratio(status.disk.free_bytes, status.disk.total_bytes);
  const diskLow = status.disk.total_bytes > 0 && diskFrac < 0.05;

  const selectedOnly = selectsGames(l.download_mode);
  const wanted = selectedOnly ? l.games - l.unselected : l.games;
  const parts: string[] = [
    selectedOnly
      ? `${l.complete.toLocaleString()} of ${plural(wanted, "selected game")} complete`
      : `${l.complete.toLocaleString()} of ${plural(l.games, "game")} complete`,
  ];
  if (d.active > 0) parts.push(`${d.active} downloading at ${formatSpeed(d.speed_bps)}`);
  else if (d.paused) parts.push("downloads paused");
  if (d.queued > 0) parts.push(`${d.queued.toLocaleString()} queued`);
  if (status.sync.running) parts.push("sync running");
  else if (status.sync.next_run_at) parts.push(`next check ${formatRelative(status.sync.next_run_at, now)}`);

  let activity: ReactNode;
  if (downloads.data) {
    activity = downloads.data.active.length ? (
      <ActiveTable items={downloads.data.active} />
    ) : (
      <EmptyState>
        {d.paused ? "Downloads are paused." : "Nothing downloading."}
        {d.queued > 0 ? ` ${plural(d.queued, "file")} queued.` : ""}
        {selectedOnly && wanted === 0 && d.queued === 0 ? (
          <>
            {" "}
            No games are selected yet. <Link to="/library">Pick games in the library</Link> to start downloading.
          </>
        ) : null}
      </EmptyState>
    );
  } else if (downloads.error) {
    activity = <ApiErrorNotice error={downloads.error} />;
  } else {
    activity = <Loading />;
  }

  return (
    <div className="stack">
      {/* Network failures are already announced by the layout strip. */}
      {!(error instanceof NetworkError) && <ApiErrorNotice error={error} stale />}
      <div>
        <h1 className="page-title">Dashboard</h1>
        <p className="summary">{parts.join(" · ")}</p>
      </div>

      <div className="overview">
        <div className="status-strip">
          <Item label="Games" value={l.games} />
          {selectedOnly && <Item label="Selected" value={wanted} />}
          <Item label="Complete" value={l.complete} />
          <Item label="Pending" value={l.pending + l.partial} />
          <Item label="Downloading" value={l.downloading} />
          <Item label="Errors" value={l.error} danger={l.error > 0} />
          <Item label="Unavailable" value={l.unavailable} />
          <Item label="Library size" value={`${formatBytes(l.bytes_done)} of ${formatBytes(l.bytes_total)}`} />
          <Item label="Disk free" value={formatBytes(status.disk.free_bytes)} danger={diskLow} />
        </div>
        <ProgressBar line value={bytesFrac} label="Library bytes downloaded" />
      </div>

      <div className="two-col">
        <section className="section">
          <div className="section-head">
            <h2>Activity</h2>
            <div className="actions">
              <PauseButton
                paused={d.paused}
                onChanged={() => {
                  refresh();
                  downloads.refresh();
                }}
              />
            </div>
          </div>
          {activity}
        </section>
        <SyncSection status={status} onChanged={refresh} />
      </div>
    </div>
  );
}

function Item({ label, value, danger }: { label: string; value: number | string; danger?: boolean }) {
  return (
    <div className="item">
      <span className="label">{label}</span>
      <span className={`value${danger ? " danger" : ""}`}>
        {typeof value === "number" ? value.toLocaleString() : value}
      </span>
    </div>
  );
}

const PHASES: Record<string, string> = {
  listing: "Listing owned games",
  artwork: "Fetching covers",
  details: "Fetching game details",
  reconciling: "Reconciling files",
};

function SyncSection({ status, onChanged }: { status: Status; onChanged: () => void }) {
  const s = status.sync;
  const sync = useToastAction(startSync, {
    success: "Library sync started",
    failure: "Could not start sync",
    onDone: onChanged,
  });
  const canSync = status.authenticated && !s.running;

  return (
    <section className="section">
      <div className="section-head">
        <h2>Sync</h2>
        <div className="actions">
          <button className="btn" onClick={() => void sync.run()} disabled={!canSync || sync.pending}>
            {sync.pending && <Spinner />} Check GOG now
          </button>
        </div>
      </div>
      <dl className="kv">
        <dt>Status</dt>
        <dd>
          {s.running ? (
            <StatusDot tone="info">
              {PHASES[s.phase] ?? "Running"}
              {s.games_total > 0 ? ` · ${s.games_done} / ${s.games_total}` : ""}
            </StatusDot>
          ) : !status.authenticated ? (
            <StatusDot tone="danger">
              <span>
                Not connected — <Link to="/auth">re-authorize</Link>
              </span>
            </StatusDot>
          ) : (
            <StatusDot tone="muted">Idle</StatusDot>
          )}
        </dd>
        <dt>Last run</dt>
        <dd>
          {s.last_finished_at ? (
            <TimeAgo iso={s.last_finished_at} />
          ) : s.last_started_at ? (
            <TimeAgo iso={s.last_started_at} prefix="started " />
          ) : (
            <span className="faint">never</span>
          )}
        </dd>
        <dt>Next run</dt>
        <dd>{s.next_run_at ? <TimeAgo iso={s.next_run_at} /> : <span className="faint">not scheduled</span>}</dd>
        <dt>Last error</dt>
        <dd className={s.last_error ? "err-text" : "faint"}>{s.last_error || "none"}</dd>
        <dt>Folder</dt>
        <dd>
          <span className="mono clip" title={status.disk.library_dir || undefined}>
            {status.disk.library_dir || "—"}
          </span>
        </dd>
      </dl>
    </section>
  );
}

function ActiveTable({ items }: { items: ActiveDownload[] }) {
  return (
    <DataTable>
      <thead>
        <tr>
          <th>Game</th>
          <th>File</th>
          <th>Progress</th>
          <th className="num">Speed</th>
          <th className="num">ETA</th>
        </tr>
      </thead>
      <tbody>
        {items.map((a) => (
          <tr key={a.file_id}>
            <td>
              <Link to={`/library/${a.game_id}`}>{a.game_title}</Link>
            </td>
            <td className="mono clip dl-file" title={a.filename}>
              {a.filename}
            </td>
            <td>
              <ProgressBar value={ratio(a.downloaded_bytes, a.size)} showPercent tone="info" />
            </td>
            <td className="num">{formatSpeed(a.speed_bps)}</td>
            <td className="num">{formatDuration(a.eta_seconds)}</td>
          </tr>
        ))}
      </tbody>
    </DataTable>
  );
}
