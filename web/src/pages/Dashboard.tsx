import { Link } from "react-router-dom";
import {
  errorMessage,
  getDownloads,
  getEstimate,
  NetworkError,
  pauseDownloads,
  resumeDownloads,
  startSync,
  type ActiveDownload,
  type SettingsEstimate,
  type Status,
} from "../api";
import { ApiErrorNotice, EmptyState, Loading, Spinner, TimeAgo } from "../components/Common";
import { DataTable } from "../components/DataTable";
import { EstimateNote } from "../components/Estimate";
import { ProgressBar } from "../components/ProgressBar";
import { StatusDot } from "../components/StatusDot";
import { useStatus } from "../components/StatusContext";
import { useToast } from "../components/Toast";
import { formatBytes, formatDuration, formatRelative, formatSpeed, plural, ratio } from "../format";
import { useAction, useNow, usePolling } from "../hooks";

export function DashboardPage() {
  const { status, error, loading, refresh } = useStatus();
  const downloads = usePolling(getDownloads, 2000);
  // The whole-library estimate reads the size catalog, which only moves as the
  // scan or a sync progresses, so it is polled far more slowly than the rest.
  const estimate = usePolling(getEstimate, 60000);
  const now = useNow();

  if (loading && !status) return <Loading text="Loading status…" />;
  if (!status) return <ApiErrorNotice error={error} />;

  const l = status.library;
  const d = status.downloads;
  const bytesFrac = ratio(l.bytes_done, l.bytes_total);
  const diskFrac = ratio(status.disk.free_bytes, status.disk.total_bytes);
  const diskLow = status.disk.total_bytes > 0 && diskFrac < 0.05;
  const remaining = Math.max(0, l.bytes_total - l.bytes_done);
  const wontFit = remaining > status.disk.free_bytes && status.disk.free_bytes > 0;

  const selectedOnly = l.download_mode === "selected";
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

  return (
    <div className="stack">
      {/* Network failures are already announced by the layout strip. */}
      {!(error instanceof NetworkError) && <ApiErrorNotice error={error} stale />}
      <div>
        <h1 className="page-title">Dashboard</h1>
        <p className="summary">{parts.join(" · ")}</p>
      </div>

      <div>
        <div className="status-strip">
          <Item label="Games" value={l.games} />
          {selectedOnly && <Item label="Selected" value={wanted} />}
          <Item label="Complete" value={l.complete} />
          <Item label="Pending" value={l.pending + l.partial} />
          <Item label="Downloading" value={l.downloading} />
          <Item label="Errors" value={l.error} danger={l.error > 0} />
          <Item label="Unavailable" value={l.unavailable} />
          <Item label="Library size" value={`${formatBytes(l.bytes_done)} of ${formatBytes(l.bytes_total)}`} />
          <Item label="Still to fetch" value={formatBytes(remaining)} danger={wontFit} />
          <Item label="Disk free" value={formatBytes(status.disk.free_bytes)} danger={diskLow} />
        </div>
        <ProgressBar line value={bytesFrac} label="Library bytes downloaded" />
        {wontFit && (
          <p className="summary err-text">
            {formatBytes(remaining)} still to fetch, but only {formatBytes(status.disk.free_bytes)} free on disk.
          </p>
        )}
        <FullBackupNote status={status} estimate={estimate.data} />
      </div>

      <div className="two-col">
        <section className="section">
          <div className="section-head">
            <h2>Activity</h2>
            <div className="actions">
              <PauseButton
                status={status}
                onChanged={() => {
                  refresh();
                  downloads.refresh();
                }}
              />
            </div>
          </div>
          {downloads.data ? (
            downloads.data.active.length ? (
              <ActiveTable items={downloads.data.active} />
            ) : (
              <EmptyState>
                {d.paused ? "Downloads are paused." : "Nothing downloading."}
                {d.queued > 0 ? ` ${plural(d.queued, "file")} queued.` : ""}
                {selectedOnly && wanted === 0 && d.queued === 0 ? (
                  <>
                    {" "}
                    No games are selected yet. <Link to="/library">Pick games in the library</Link> to start
                    downloading.
                  </>
                ) : null}
              </EmptyState>
            )
          ) : downloads.error ? (
            <ApiErrorNotice error={downloads.error} />
          ) : (
            <Loading />
          )}
        </section>
        <SyncSection status={status} onChanged={refresh} />
      </div>
    </div>
  );
}

/**
 * What the whole library would cost under the settings in force. Only worth
 * saying while downloading a selection: the figures above already cover the
 * whole library in the "all" mode, and would just be repeated here.
 */
function FullBackupNote({ status, estimate }: { status: Status; estimate: SettingsEstimate | null }) {
  if (!estimate || !status.setup_complete || status.library.download_mode !== "selected") return null;
  const free = status.disk.free_bytes;
  return (
    <>
      <EstimateNote estimate={estimate} />
      {estimate.bytes > free && free > 0 && estimate.games_scanned > 0 && (
        <p className="summary faint">
          That is more than the {formatBytes(free)} free on disk. <Link to="/settings">Settings</Link> costs
          other combinations of platforms, languages and extras.
        </p>
      )}
    </>
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
  details: "Fetching game details",
  reconciling: "Reconciling files",
};

function SyncSection({ status, onChanged }: { status: Status; onChanged: () => void }) {
  const s = status.sync;
  const toast = useToast();
  const sync = useAction(startSync);
  const canSync = status.authenticated && !s.running;

  const run = async () => {
    try {
      await sync.run();
      toast.success("Library sync started");
      onChanged();
    } catch (e) {
      toast.error(`Could not start sync: ${errorMessage(e)}`);
    }
  };

  return (
    <section className="section">
      <div className="section-head">
        <h2>Sync</h2>
        <div className="actions">
          <button className="btn" onClick={run} disabled={!canSync || sync.pending}>
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
      </dl>
      <div className="mono muted" title="Library folder">
        {status.disk.library_dir || "—"}
      </div>
    </section>
  );
}

function PauseButton({ status, onChanged }: { status: Status; onChanged: () => void }) {
  const toast = useToast();
  const paused = status.downloads.paused;
  const toggle = useAction(paused ? resumeDownloads : pauseDownloads);
  const run = async () => {
    try {
      const res = (await toggle.run()) as { paused: boolean };
      toast.success(res.paused ? "Downloads paused" : "Downloads resumed");
      onChanged();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };
  return (
    <button className="btn" onClick={run} disabled={toggle.pending}>
      {paused ? "Resume" : "Pause"}
    </button>
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
