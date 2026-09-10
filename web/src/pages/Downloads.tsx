import { Link } from "react-router-dom";
import { errorMessage, getDownloads, pauseDownloads, resumeDownloads } from "../api";
import { ApiErrorNotice, EmptyState, Loading } from "../components/Common";
import { IconPause, IconPlay } from "../components/Icons";
import { ProgressBar } from "../components/ProgressBar";
import { useStatus } from "../components/StatusContext";
import { useToast } from "../components/Toast";
import { formatBytes, formatDuration, formatSpeed, platformLabel, ratio } from "../format";
import { useAction, usePolling } from "../hooks";

export function DownloadsPage() {
  const dls = usePolling(getDownloads, 2000);
  const { status, refresh: refreshStatus } = useStatus();
  const toast = useToast();
  const paused = dls.data?.paused ?? status?.downloads.paused ?? false;
  const toggle = useAction(paused ? resumeDownloads : pauseDownloads);

  const run = async () => {
    try {
      const res = (await toggle.run()) as { paused: boolean };
      toast.success(res.paused ? "Downloads paused" : "Downloads resumed");
      dls.refresh();
      refreshStatus();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  const data = dls.data;
  const totalSpeed = data?.active.reduce((s, a) => s + a.speed_bps, 0) ?? status?.downloads.speed_bps ?? 0;

  return (
    <div className="stack">
      <div className="page-head">
        <h1>Downloads</h1>
        {paused ? <span className="badge amber">Paused</span> : <span className="badge grey">Running</span>}
        <span className="muted small num">{formatSpeed(totalSpeed)}</span>
        <button className="btn" onClick={run} disabled={toggle.pending}>
          {paused ? (
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
      <ApiErrorNotice error={dls.error} stale={!!data} />

      {!data && dls.loading ? (
        <Loading text="Loading downloads…" />
      ) : data ? (
        <>
          <section className="stack">
            <h3>Active ({data.active.length})</h3>
            {data.active.length === 0 ? (
              <EmptyState compact title={paused ? "Downloads are paused" : "Nothing downloading right now"}>
                {paused ? "Resume to continue with the queue." : "Queued files start automatically."}
              </EmptyState>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th>Game</th>
                      <th>File</th>
                      <th className="num">Size</th>
                      <th>Progress</th>
                      <th className="num">Speed</th>
                      <th className="num">ETA</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.active.map((a) => {
                      const frac = ratio(a.downloaded_bytes, a.size);
                      return (
                        <tr key={a.file_id}>
                          <td className="wrap">
                            <Link to={`/library/${a.game_id}`}>{a.game_title}</Link>
                          </td>
                          <td className="mono" title={a.filename}>
                            {a.filename}
                          </td>
                          <td className="num">{formatBytes(a.size)}</td>
                          <td className="progress-cell">
                            <ProgressBar value={frac} size="sm" tone="blue" striped showPercent />
                            <span className="small muted num">{formatBytes(a.downloaded_bytes)}</span>
                          </td>
                          <td className="num">{formatSpeed(a.speed_bps)}</td>
                          <td className="num">{formatDuration(a.eta_seconds)}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </section>

          <section className="stack">
            <h3>
              Queue ({data.queued_total.toLocaleString()}
              {data.queue.length < data.queued_total ? `, showing first ${data.queue.length}` : ""})
            </h3>
            {data.queue.length === 0 ? (
              <EmptyState compact title="Queue is empty">Everything wanted has been downloaded.</EmptyState>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th className="num">#</th>
                      <th>Game</th>
                      <th>Name</th>
                      <th>OS</th>
                      <th className="num">Size</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.queue.map((q, i) => (
                      <tr key={q.file_id}>
                        <td className="num faint">{i + 1}</td>
                        <td className="wrap">
                          <Link to={`/library/${q.game_id}`}>{q.game_title}</Link>
                        </td>
                        <td className="wrap">{q.name}</td>
                        <td>{platformLabel(q.os)}</td>
                        <td className="num">{formatBytes(q.size)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      ) : null}
    </div>
  );
}
