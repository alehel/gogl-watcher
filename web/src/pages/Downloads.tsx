import { Link } from "react-router-dom";
import { getDownloads } from "../api";
import { ApiErrorNotice, EmptyState, Loading } from "../components/Common";
import { DataTable } from "../components/DataTable";
import { PauseButton } from "../components/PauseButton";
import { ProgressBar } from "../components/ProgressBar";
import { useStatus } from "../components/StatusContext";
import { formatBytes, formatDuration, formatSpeed, platformLabel, ratio } from "../format";
import { usePolling } from "../hooks";

export function DownloadsPage() {
  const dls = usePolling(getDownloads, 2000);
  const { status, refresh: refreshStatus } = useStatus();
  const paused = dls.data?.paused ?? status?.downloads.paused ?? false;

  const data = dls.data;
  const totalSpeed = data?.active.reduce((s, a) => s + a.speed_bps, 0) ?? status?.downloads.speed_bps ?? 0;
  const hidden = data ? data.queued_total - data.queue.length : 0;

  return (
    <div className="stack">
      <div className="page-head">
        <h1 className="page-title">Downloads</h1>
        {data && data.active.length > 0 && <span className="muted num">{formatSpeed(totalSpeed)}</span>}
        <div className="actions">
          <PauseButton
            paused={paused}
            onChanged={() => {
              dls.refresh();
              refreshStatus();
            }}
          />
        </div>
      </div>
      {paused && (
        <div className="strip warn" role="status">
          Downloads are paused. Queued files wait until you resume.
        </div>
      )}
      <ApiErrorNotice error={dls.error} stale={!!data} />

      {!data && dls.loading && <Loading text="Loading downloads…" />}
      {data && (
        <>
          <section className="section">
            <div className="section-head">
              <h2>Active</h2>
              <span className="meta num">{data.active.length}</span>
            </div>
            {data.active.length === 0 ? (
              <EmptyState>{paused ? "Nothing downloading while paused." : "Nothing downloading."}</EmptyState>
            ) : (
              <DataTable>
                <thead>
                  <tr>
                    <th>Game</th>
                    <th>File</th>
                    <th className="num">Size</th>
                    <th>Progress</th>
                    <th className="num">Downloaded</th>
                    <th className="num">Speed</th>
                    <th className="num">ETA</th>
                  </tr>
                </thead>
                <tbody>
                  {data.active.map((a) => (
                    <tr key={a.file_id}>
                      <td className="wrap">
                        <Link to={`/library/${a.game_id}`}>{a.game_title}</Link>
                      </td>
                      <td className="mono clip" title={a.filename}>
                        {a.filename}
                      </td>
                      <td className="num">{formatBytes(a.size)}</td>
                      <td>
                        <ProgressBar value={ratio(a.downloaded_bytes, a.size)} showPercent tone="info" />
                      </td>
                      <td className="num">{formatBytes(a.downloaded_bytes)}</td>
                      <td className="num">{formatSpeed(a.speed_bps)}</td>
                      <td className="num">{formatDuration(a.eta_seconds)}</td>
                    </tr>
                  ))}
                </tbody>
              </DataTable>
            )}
          </section>

          <section className="section">
            <div className="section-head">
              <h2>Queue</h2>
              <span className="meta num">{data.queued_total.toLocaleString()}</span>
            </div>
            {data.queue.length === 0 ? (
              <EmptyState>The queue is empty. Everything wanted has been downloaded.</EmptyState>
            ) : (
              <>
                <DataTable>
                  <thead>
                    <tr>
                      <th>Game</th>
                      <th>Name</th>
                      <th>OS</th>
                      <th className="num">Size</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.queue.map((q) => (
                      <tr key={q.file_id}>
                        <td className="wrap">
                          <Link to={`/library/${q.game_id}`}>{q.game_title}</Link>
                        </td>
                        <td className="wrap">{q.name}</td>
                        <td>{platformLabel(q.os)}</td>
                        <td className="num">{formatBytes(q.size)}</td>
                      </tr>
                    ))}
                  </tbody>
                </DataTable>
                {hidden > 0 && <p className="faint small">and {hidden.toLocaleString()} more</p>}
              </>
            )}
          </section>
        </>
      )}
    </div>
  );
}
