import { useCallback, useEffect, useState } from "react";
import { errorMessage, getLogs, type LogEntry, type LogLevel } from "../api";
import { ApiErrorNotice, EmptyState, Loading, Spinner } from "../components/Common";
import { useToast } from "../components/Toast";
import { formatAbsolute, formatLogTime } from "../format";
import { useDebounced, usePolling } from "../hooks";

const LEVELS: Array<{ value: LogLevel; label: string }> = [
  { value: "debug", label: "Debug and up (all)" },
  { value: "info", label: "Info and up" },
  { value: "warn", label: "Warnings and up" },
  { value: "error", label: "Errors only" },
];

/** Prepends the newest page, keeping already-loaded older rows below it. */
function merge(newest: LogEntry[], prev: LogEntry[]): LogEntry[] {
  if (newest.length === 0) return [];
  const minId = newest[newest.length - 1].id;
  return [...newest, ...prev.filter((r) => r.id < minId)];
}

export function LogsPage() {
  const toast = useToast();
  const [level, setLevel] = useState<LogLevel>("info");
  const [q, setQ] = useState("");
  const dq = useDebounced(q.trim(), 300);
  const [auto, setAuto] = useState(true);
  const [rows, setRows] = useState<LogEntry[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);

  const newest = usePolling(() => getLogs({ level, q: dq }), auto ? 3000 : 0, [level, dq]);

  // Filter changed: drop everything until the new first page arrives.
  useEffect(() => {
    setRows([]);
    setHasMore(false);
  }, [level, dq]);

  useEffect(() => {
    if (!newest.data) return;
    const page = newest.data;
    setRows((prev) => {
      const merged = merge(page.logs, prev);
      // Only trust the first page's has_more when we hold nothing older than it.
      if (merged.length === page.logs.length) setHasMore(page.has_more);
      return merged;
    });
  }, [newest.data]);

  const loadOlder = useCallback(async () => {
    if (rows.length === 0) return;
    const before = rows[rows.length - 1].id;
    setLoadingOlder(true);
    try {
      const res = await getLogs({ level, q: dq, before_id: before });
      setRows((prev) => [...prev, ...res.logs.filter((r) => r.id < before)]);
      setHasMore(res.has_more);
    } catch (e) {
      toast.error(`Could not load older logs: ${errorMessage(e)}`);
    } finally {
      setLoadingOlder(false);
    }
  }, [rows, level, dq, toast]);

  return (
    <div className="stack">
      <div className="page-head">
        <h1 className="page-title">Logs</h1>
      </div>
      <div className="toolbar">
        <select
          className="select"
          value={level}
          onChange={(e) => setLevel(e.target.value as LogLevel)}
          aria-label="Minimum level"
        >
          {LEVELS.map((l) => (
            <option key={l.value} value={l.value}>
              {l.label}
            </option>
          ))}
        </select>
        <input
          className="input search"
          type="search"
          placeholder="Search messages"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          aria-label="Search logs"
        />
        <label className="toggle">
          <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
          Auto-refresh
        </label>
        {!auto && (
          <button className="btn" onClick={newest.refresh}>
            Refresh
          </button>
        )}
      </div>

      <ApiErrorNotice error={newest.error} stale={rows.length > 0} />

      {newest.loading && rows.length === 0 ? (
        <Loading text="Loading logs…" />
      ) : rows.length === 0 ? (
        <EmptyState>{dq ? "Nothing matches the search." : "Nothing has been logged at this level yet."}</EmptyState>
      ) : (
        <div>
          <div className="log-list">
            {rows.map((r) => (
              <div key={r.id} className="log-row">
                <span className="ts" title={formatAbsolute(r.ts)}>
                  {formatLogTime(r.ts)}
                </span>
                <span className={`lvl ${r.level}`}>{r.level}</span>
                <span className="comp" title={r.component}>
                  {r.component}
                </span>
                <span className="msg">{r.message}</span>
              </div>
            ))}
          </div>
          <div className="log-foot">
            {hasMore ? (
              <button className="btn" onClick={loadOlder} disabled={loadingOlder}>
                {loadingOlder && <Spinner />} Load older
              </button>
            ) : (
              <span className="faint small">Beginning of log</span>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
