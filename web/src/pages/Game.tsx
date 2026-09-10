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
import { DataTable } from "../components/DataTable";
import { IconBack } from "../components/Icons";
import { ProgressBar } from "../components/ProgressBar";
import { useGameSelection } from "../components/Selection";
import { FileStatusDot, GameStatusDot, gameStatusTone } from "../components/StatusDot";
import { useStatus } from "../components/StatusContext";
import { useToast } from "../components/Toast";
import { formatBytes, formatRelative, formatSpeed, platformLabel, platformsText, plural, ratio } from "../format";
import { useAction, useNow, usePolling } from "../hooks";

export function GamePage() {
  const { id = "" } = useParams();
  const toast = useToast();
  const { status, refresh: refreshStatus } = useStatus();
  const game = usePolling(() => getGame(id), 2000, [id]);
  const [broken, setBroken] = useState(false);
  const now = useNow();

  const sync = useAction(() => syncGame(id));
  const retry = useAction(() => retryGame(id));
  const selection = useGameSelection(() => {
    game.refresh();
    refreshStatus();
  });
  const selectedOnly = status?.library.download_mode === "selected";

  const back = (
    <Link to="/library" className="back">
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
          <EmptyState>
            This game is not in the library (anymore). <Link to="/library">Back to the library</Link>
          </EmptyState>
        ) : (
          <ApiErrorNotice error={game.error} />
        )}
      </div>
    );
  }

  const { game: g, products } = game.data;
  const unselected = g.status === "unselected";
  const hasErrors = products.some((p) => p.files.some((f) => f.status === "error"));
  const sorted = [...products].sort((a, b) => Number(a.is_dlc) - Number(b.is_dlc) || a.title.localeCompare(b.title));
  const showImg = !!g.image && !broken;

  const meta = [
    platformsText(g.works_on, true) || null,
    ...(unselected
      ? []
      : [
          plural(g.files_total, "file"),
          formatBytes(g.bytes_total),
          g.last_synced_at ? `synced ${formatRelative(g.last_synced_at, now)}` : "never synced",
        ]),
  ].filter(Boolean);

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
      {selection.modal}

      <div className="game-head">
        <div className="game-cover">
          {showImg && <img src={g.image ?? undefined} alt="" onError={() => setBroken(true)} />}
        </div>
        <div className="game-info">
          <h1 className="page-title">{g.title}</h1>
          <div className="muted">
            <GameStatusDot status={g.status} /> · {meta.join(" · ")}
          </div>
          <div className="mono path">{g.folder || "—"}</div>
          {g.files_total > 0 && (
            <ProgressBar line value={g.progress} tone={gameStatusTone(g.status)} label="Overall progress" />
          )}
          <div className="btn-row">
            {selectedOnly &&
              (g.selected ? (
                <button
                  className="btn"
                  onClick={() => void selection.setSelection([g.id], false)}
                  disabled={selection.busy}
                >
                  {selection.busy && <Spinner />} Remove from selection
                </button>
              ) : (
                <button
                  className="btn primary"
                  onClick={() => void selection.setSelection([g.id], true)}
                  disabled={selection.busy}
                >
                  {selection.busy && <Spinner />} Select for download
                </button>
              ))}
            {!unselected && (
              <button className="btn" onClick={doSync} disabled={sync.pending}>
                {sync.pending && <Spinner />} Re-check on GOG
              </button>
            )}
            {hasErrors && (
              <button className="btn danger" onClick={doRetry} disabled={retry.pending}>
                {retry.pending && <Spinner />} Retry failed
              </button>
            )}
          </div>
        </div>
      </div>

      {unselected ? (
        <EmptyState>
          This game is not selected for download. Select it to fetch its installers
          {products.some((p) => p.files.length > 0) ? "; files kept from before are listed below as inactive." : "."}
        </EmptyState>
      ) : null}
      {unselected && !products.some((p) => p.files.length > 0) ? null : sorted.length === 0 ? (
        <EmptyState>
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
    <section className="section product">
      <div className="section-head">
        <h2 className="section-title">
          {product.title}
          {product.is_dlc && <span className="dlc">DLC</span>}
        </h2>
        <span className="meta num">
          {done} of {active} files
        </span>
      </div>
      {product.files.length === 0 ? (
        <EmptyState>Nothing matches the chosen platforms and languages.</EmptyState>
      ) : (
        <DataTable>
          <thead>
            <tr>
              <th>Type</th>
              <th>OS</th>
              <th>Lang</th>
              <th>Name</th>
              <th>Version</th>
              <th className="num">Size</th>
              <th>File</th>
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
        </DataTable>
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
  const isError = f.status === "error";
  const dl = f.progress?.downloaded_bytes ?? 0;
  return (
    <tr className={isError ? "error" : f.status === "inactive" ? "dim" : ""}>
      <td>{f.kind === "installer" ? "Installer" : "Extra"}</td>
      <td>{platformLabel(f.os)}</td>
      <td className="mono">{f.language || "—"}</td>
      <td className="clip" title={f.name}>
        {f.name}
      </td>
      <td className="mono">{f.version || "—"}</td>
      <td className="num">{formatBytes(f.size)}</td>
      <td className="mono clip" title={f.local_path ?? f.filename ?? undefined}>
        {f.filename ?? <span className="faint">—</span>}
      </td>
      <td className={isError ? "two-line" : ""}>
        <FileStatusDot status={f.status} />
        {isError && (
          <span className="sub err-text">
            {f.error || "Download failed"}{" "}
            <button className="link-btn" onClick={doRetry} disabled={retry.pending}>
              Retry
            </button>
          </span>
        )}
      </td>
      <td>
        {downloading ? (
          <div className="bar-row">
            <ProgressBar value={ratio(dl, f.size)} showPercent tone="info" />
            <span className="pct">{formatSpeed(f.progress?.speed_bps)}</span>
          </div>
        ) : (
          <span className="faint">—</span>
        )}
      </td>
      <td className="muted">{f.downloaded_at ? <TimeAgo iso={f.downloaded_at} /> : <span className="faint">—</span>}</td>
    </tr>
  );
}
