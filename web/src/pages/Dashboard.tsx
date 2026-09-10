import { Link } from "react-router-dom";
import {
  errorMessage,
  getDownloads,
  pauseDownloads,
  resumeDownloads,
  startSync,
  type ActiveDownload,
  type Status,
} from "../api";
import { ApiErrorNotice, EmptyState, Loading, TimeAgo } from "../components/Common";
import { IconDisk, IconDownload, IconLibrary, IconPause, IconPlay, IconRefresh } from "../components/Icons";
import { ProgressBar } from "../components/ProgressBar";
import { useStatus } from "../components/StatusContext";
import { useToast } from "../components/Toast";
import { formatBytes, formatDuration, formatPercent, formatSpeed, ratio } from "../format";
import { useAction, usePolling } from "../hooks";

export function DashboardPage() {
  const { status, error, loading, refresh } = useStatus();
  const downloads = usePolling(getDownloads, 2000);

  if (loading && !status) return <Loading text="Loading status…" />;
  if (!status) return <ApiErrorNotice error={error} />;

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Dashboard</h1>
      </div>
      <ApiErrorNotice error={error} stale />
      <div className="grid two">
        <LibraryCard status={status} />
        <DiskCard status={status} />
        <SyncCard status={status} onChanged={refresh} />
        <DownloadsCard
          status={status}
          onChanged={() => {
            refresh();
            downloads.refresh();
          }}
        />
      </div>
      <div className="card">
        <div className="card-head">
          <h2>
            <IconDownload /> Active downloads
          </h2>
          <Link to="/downloads" className="small">
            View queue
          </Link>
        </div>
        {downloads.data ? (
          downloads.data.active.length ? (
            <ActiveList items={downloads.data.active} />
          ) : (
            <EmptyState compact title={status.downloads.paused ? "Downloads are paused" : "Nothing downloading"}>
              {status.downloads.queued > 0
                ? `${status.downloads.queued} files are queued.`
                : "New files show up here as soon as a sync finds them."}
            </EmptyState>
          )
        ) : downloads.error ? (
          <ApiErrorNotice error={downloads.error} />
        ) : (
          <Loading />
        )}
      </div>
    </div>
  );
}

function LibraryCard({ status }: { status: Status }) {
  const l = status.library;
  const frac = ratio(l.bytes_done, l.bytes_total);
  return (
    <div className="card">
      <div className="card-head">
        <h2>
          <IconLibrary /> Library
        </h2>
        <Link to="/library" className="small">
          Browse
        </Link>
      </div>
      <div className="stack">
        <div className="stats">
          <Stat label="Games" value={l.games} />
          <Stat label="Complete" value={l.complete} tone="green" />
          <Stat label="Pending" value={l.pending + l.partial} tone="amber" />
          <Stat label="Downloading" value={l.downloading} tone="blue" />
          <Stat label="Errors" value={l.error} tone="red" />
          <Stat label="Unavailable" value={l.unavailable} tone="grey" />
        </div>
        <div>
          <div className="progress-row small muted" style={{ marginBottom: 4 }}>
            <span className="num">
              {formatBytes(l.bytes_done)} of {formatBytes(l.bytes_total)}
            </span>
            <span style={{ marginLeft: "auto" }} className="num">
              {formatPercent(frac)}
            </span>
          </div>
          <ProgressBar value={frac} size="lg" tone={frac >= 1 && l.bytes_total > 0 ? "green" : "accent"} />
        </div>
      </div>
    </div>
  );
}

function DiskCard({ status }: { status: Status }) {
  const d = status.disk;
  const used = Math.max(0, d.total_bytes - d.free_bytes);
  const frac = ratio(used, d.total_bytes);
  const tone = frac > 0.95 ? "red" : frac > 0.85 ? "amber" : "accent";
  return (
    <div className="card">
      <div className="card-head">
        <h2>
          <IconDisk /> Disk
        </h2>
      </div>
      <div className="stack">
        <div className="stats">
          <Stat label="Free" value={formatBytes(d.free_bytes)} tone={frac > 0.95 ? "red" : undefined} />
          <Stat label="Used" value={formatBytes(used)} />
          <Stat label="Total" value={formatBytes(d.total_bytes)} />
        </div>
        <ProgressBar value={frac} size="lg" tone={tone} label="Disk usage" />
        <dl className="kv">
          <dt>Library folder</dt>
          <dd className="mono">{d.library_dir || "—"}</dd>
        </dl>
      </div>
    </div>
  );
}

const PHASES: Record<string, string> = {
  listing: "Listing owned games",
  details: "Fetching game details",
  reconciling: "Reconciling files",
};

function SyncCard({ status, onChanged }: { status: Status; onChanged: () => void }) {
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
    <div className="card">
      <div className="card-head">
        <h2>
          <IconRefresh /> Sync
          {s.running ? <span className="badge blue">Running</span> : <span className="badge grey">Idle</span>}
        </h2>
        <button className="btn sm" onClick={run} disabled={!canSync || sync.pending}>
          <IconRefresh /> Sync now
        </button>
      </div>
      <div className="stack">
        {s.running && (
          <div>
            <div className="small muted" style={{ marginBottom: 4 }}>
              {PHASES[s.phase] ?? "Working"}
              {s.games_total > 0 && (
                <span className="num">
                  {" "}
                  — {s.games_done} / {s.games_total} games
                </span>
              )}
            </div>
            <ProgressBar
              value={s.games_total > 0 ? ratio(s.games_done, s.games_total) : 0}
              tone="blue"
              striped
            />
          </div>
        )}
        <dl className="kv">
          <dt>Last run</dt>
          <dd>
            {s.last_finished_at ? (
              <TimeAgo iso={s.last_finished_at} />
            ) : s.last_started_at ? (
              <TimeAgo iso={s.last_started_at} prefix="started " />
            ) : (
              <span className="muted">never</span>
            )}
          </dd>
          <dt>Next run</dt>
          <dd>{s.next_run_at ? <TimeAgo iso={s.next_run_at} /> : <span className="muted">not scheduled</span>}</dd>
          {s.last_error && (
            <>
              <dt>Last error</dt>
              <dd className="err-text">{s.last_error}</dd>
            </>
          )}
        </dl>
        {!status.authenticated && (
          <div className="small err-text">
            Not connected to GOG — <Link to="/auth">re-authorize</Link> to sync.
          </div>
        )}
      </div>
    </div>
  );
}

function DownloadsCard({ status, onChanged }: { status: Status; onChanged: () => void }) {
  const d = status.downloads;
  const toast = useToast();
  const toggle = useAction(d.paused ? resumeDownloads : pauseDownloads);
  const run = async () => {
    try {
      const res = await toggle.run();
      const paused = (res as { paused: boolean }).paused;
      toast.success(paused ? "Downloads paused" : "Downloads resumed");
      onChanged();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };
  return (
    <div className="card">
      <div className="card-head">
        <h2>
          <IconDownload /> Downloads
          {d.paused ? (
            <span className="badge amber">Paused</span>
          ) : d.active > 0 ? (
            <span className="badge blue">Active</span>
          ) : (
            <span className="badge grey">Idle</span>
          )}
        </h2>
        <button className="btn sm" onClick={run} disabled={toggle.pending}>
          {d.paused ? (
            <>
              <IconPlay /> Resume
            </>
          ) : (
            <>
              <IconPause /> Pause
            </>
          )}
        </button>
      </div>
      <div className="stats">
        <Stat label="Active" value={d.active} tone={d.active > 0 ? "blue" : undefined} />
        <Stat label="Queued" value={d.queued} tone={d.queued > 0 ? "amber" : undefined} />
        <Stat label="Speed" value={formatSpeed(d.speed_bps)} />
      </div>
    </div>
  );
}

function Stat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number | string;
  tone?: "green" | "blue" | "amber" | "red" | "grey";
}) {
  return (
    <div className={`stat ${tone ? `c-${tone}` : ""}`}>
      <div className="label">{label}</div>
      <div className="value">{typeof value === "number" ? value.toLocaleString() : value}</div>
    </div>
  );
}

export function ActiveList({ items }: { items: ActiveDownload[] }) {
  return (
    <div className="dl-list">
      {items.map((a) => {
        const frac = ratio(a.downloaded_bytes, a.size);
        return (
          <div key={a.file_id} className="dl-item">
            <div className="row">
              <Link className="name" to={`/library/${a.game_id}`} title={a.game_title}>
                {a.game_title}
              </Link>
              <span className="meta">
                {formatBytes(a.downloaded_bytes)} / {formatBytes(a.size)} · {formatSpeed(a.speed_bps)} · ETA{" "}
                {formatDuration(a.eta_seconds)}
              </span>
            </div>
            <div className="file" title={a.filename}>
              {a.filename}
            </div>
            <ProgressBar value={frac} tone="blue" showPercent striped />
          </div>
        );
      })}
    </div>
  );
}
