import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  ApiError,
  errorMessage,
  getGame,
  retryFile,
  retryGame,
  syncGame,
  type GameFile,
  type Product,
} from "../api";
import { ApiErrorNotice, EmptyState, Loading, Spinner, TimeAgo } from "../components/Common";
import { IconBack, IconImage, IconRefresh } from "../components/Icons";
import { ProgressBar } from "../components/ProgressBar";
import { FileStatusBadge, GameStatusBadge } from "../components/StatusBadge";
import { useToast } from "../components/Toast";
import { formatAbsolute, formatBytes, formatSpeed, PLATFORM_LABELS, platformLabel, ratio } from "../format";
import { useAction, usePolling } from "../hooks";

export function GamePage() {
  const { id = "" } = useParams();
  const toast = useToast();
  const game = usePolling(() => getGame(id), 2000, [id]);
  const [broken, setBroken] = useState(false);

  const sync = useAction(() => syncGame(id));
  const retry = useAction(() => retryGame(id));

  const back = (
    <Link to="/library" className="btn ghost sm">
      <IconBack /> Library
    </Link>
  );

  if (game.loading && !game.data) {
    return (
      <div className="stack">
        {back}
        <Loading text="Loading game…" />
      </div>
    );
  }
  if (!game.data) {
    const notFound = game.error instanceof ApiError && game.error.status === 404;
    return (
      <div className="stack">
        {back}
        {notFound ? (
          <EmptyState title="Game not found">This game is not in the library (anymore).</EmptyState>
        ) : (
          <ApiErrorNotice error={game.error} />
        )}
      </div>
    );
  }

  const { game: g, products } = game.data;
  const hasErrors = products.some((p) => p.files.some((f) => f.status === "error"));
  const sorted = [...products].sort((a, b) => Number(a.is_dlc) - Number(b.is_dlc) || a.title.localeCompare(b.title));
  const showImg = !!g.image && !broken;
  const platforms = (Object.keys(PLATFORM_LABELS) as Array<keyof typeof g.works_on>).filter((p) => g.works_on?.[p]);

  const doSync = async () => {
    try {
      await sync.run();
      toast.success("Re-checking on GOG…");
      game.refresh();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };
  const doRetry = async () => {
    try {
      await retry.run();
      toast.success("Failed files queued again");
      game.refresh();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  return (
    <div className="stack">
      <div>{back}</div>
      <ApiErrorNotice error={game.error} stale />

      <div className="game-head">
        <div className={`cover ${showImg ? "" : "placeholder"}`}>
          {showImg ? <img src={g.image ?? undefined} alt="" onError={() => setBroken(true)} /> : <IconImage />}
        </div>
        <div className="info">
          <div className="title-row">
            <h1>{g.title}</h1>
            <GameStatusBadge status={g.status} />
          </div>
          <dl className="kv">
            <dt>Works on</dt>
            <dd>
              {platforms.length ? (
                <span className="inline-list">
                  {platforms.map((p) => (
                    <span key={p} className="tag">
                      {PLATFORM_LABELS[p]}
                    </span>
                  ))}
                </span>
              ) : (
                <span className="muted">unknown</span>
              )}
            </dd>
            <dt>Folder</dt>
            <dd className="mono">{g.folder || "—"}</dd>
            <dt>Files</dt>
            <dd className="num">
              {g.files_done} of {g.files_total} · {formatBytes(g.bytes_done)} of {formatBytes(g.bytes_total)}
            </dd>
            <dt>Last synced</dt>
            <dd>
              <TimeAgo iso={g.last_synced_at} />
            </dd>
          </dl>
          {g.status !== "complete" && g.files_total > 0 && (
            <ProgressBar value={g.progress} showPercent striped={g.status === "downloading"} />
          )}
          <div className="btn-row">
            <button className="btn" onClick={doSync} disabled={sync.pending}>
              {sync.pending ? <Spinner /> : <IconRefresh />} Re-check on GOG
            </button>
            {hasErrors && (
              <button className="btn danger" onClick={doRetry} disabled={retry.pending}>
                {retry.pending && <Spinner />} Retry failed
              </button>
            )}
          </div>
        </div>
      </div>

      {sorted.length === 0 ? (
        <EmptyState title="No files known yet">
          {g.status === "unsynced"
            ? "Details for this game have not been fetched yet. Use “Re-check on GOG” to fetch them now."
            : "GOG offers no files for this game that match the chosen platforms and languages."}
        </EmptyState>
      ) : (
        sorted.map((p) => <ProductSection key={p.id} product={p} onChanged={game.refresh} />)
      )}
    </div>
  );
}

function ProductSection({ product, onChanged }: { product: Product; onChanged: () => void }) {
  const done = product.files.filter((f) => f.status === "done").length;
  const active = product.files.filter((f) => f.status !== "inactive").length;
  return (
    <section className="product-section">
      <div className="section-head">
        <h2>{product.title}</h2>
        <span className={`badge plain ${product.is_dlc ? "accent" : "grey"}`}>{product.is_dlc ? "DLC" : "Base game"}</span>
        <span className="muted small num">
          {done} of {active} files
        </span>
      </div>
      {product.files.length === 0 ? (
        <EmptyState compact title="No files">
          Nothing matches the chosen platforms and languages.
        </EmptyState>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Kind</th>
                <th>OS</th>
                <th>Lang</th>
                <th>Name</th>
                <th>Version</th>
                <th className="num">Size</th>
                <th>Filename</th>
                <th>Status</th>
                <th>Progress</th>
                <th>Downloaded</th>
              </tr>
            </thead>
            <tbody>
              {product.files.map((f) => (
                <FileRow key={f.id} file={f} onChanged={onChanged} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function FileRow({ file: f, onChanged }: { file: GameFile; onChanged: () => void }) {
  const toast = useToast();
  const retry = useAction(() => retryFile(f.id));
  const doRetry = async () => {
    try {
      await retry.run();
      toast.success("File queued again");
      onChanged();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };
  const downloading = f.status === "downloading";
  const dl = f.progress?.downloaded_bytes ?? 0;
  return (
    <tr className={f.status === "inactive" ? "muted" : ""}>
      <td>
        <span className="tag">{f.kind}</span>
      </td>
      <td>{platformLabel(f.os)}</td>
      <td className="mono">{f.language || "—"}</td>
      <td className="wrap" title={f.name}>
        {f.name}
      </td>
      <td className="mono">{f.version || "—"}</td>
      <td className="num">{formatBytes(f.size)}</td>
      <td className="mono clip" title={f.local_path ?? f.filename ?? undefined}>
        {f.filename ?? <span className="faint">—</span>}
      </td>
      <td>
        <FileStatusBadge status={f.status} />
      </td>
      <td className="progress-cell">
        {downloading ? (
          <>
            <ProgressBar value={ratio(dl, f.size)} size="sm" tone="blue" striped />
            <span className="small muted num">
              {formatBytes(dl)} · {formatSpeed(f.progress?.speed_bps)}
            </span>
          </>
        ) : f.status === "error" ? (
          <div className="error-cell">
            <span className="err-text small">{f.error || "Download failed"}</span>
            <button className="btn sm" onClick={doRetry} disabled={retry.pending}>
              {retry.pending ? <Spinner /> : <IconRefresh />} Retry
            </button>
          </div>
        ) : f.status === "done" ? (
          <span className="ok-text small">100%</span>
        ) : (
          <span className="faint">—</span>
        )}
      </td>
      <td>
        {f.downloaded_at ? (
          <span title={formatAbsolute(f.downloaded_at)}>
            <TimeAgo iso={f.downloaded_at} />
          </span>
        ) : (
          <span className="faint">—</span>
        )}
      </td>
    </tr>
  );
}
