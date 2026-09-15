import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  ApiError,
  getGame,
  getGameOffer,
  retryFile,
  retryGame,
  syncGame,
  type GameFile,
  type GameSummary,
  type Offer,
  type OfferItem,
  type OfferProduct,
  type Product,
  selectsGames,
} from "../api";
import { ApiErrorNotice, CoverImage, EmptyState, Loading, Spinner, TimeAgo } from "../components/Common";
import { DataTable } from "../components/DataTable";
import { IconBack } from "../components/Icons";
import { ProgressBar } from "../components/ProgressBar";
import { useGameOptions, type GameOptionsValue } from "../components/GameOptions";
import { useGameSelection } from "../components/Selection";
import { FileStatusDot, GameStatusDot, gameStatusTone } from "../components/StatusDot";
import { useStatus } from "../components/StatusContext";
import { useToastAction } from "../components/Toast";
import { TagList } from "./Library";
import { formatBytes, formatRelative, formatSpeed, platformLabel, platformsText, plural, ratio } from "../format";
import { IconCheck } from "../components/Icons";
import { useNow, usePolling, type PollingState } from "../hooks";
import { ArtworkSection } from "../components/ArtworkSection";
import { mockArtworkOffer } from "../mock/artwork";

export function GamePage() {
  const { id = "" } = useParams();
  const { status, refresh: refreshStatus } = useStatus();
  const game = usePolling(() => getGame(id), 2000, [id]);
  const now = useNow();
  // A game that is not selected has nothing planned, so its page asks GOG what
  // selecting it would fetch. Polling the game keeps its status current; the
  // offer is loaded once, and the server caches it for a while.
  const unselected = game.data?.game.status === "unselected";
  const offer = usePolling(() => getGameOffer(id), 0, [id], unselected);

  const sync = useToastAction(() => syncGame(id), { success: "Re-checking on GOG…", onDone: game.refresh });
  const retry = useToastAction(() => retryGame(id), { success: "Failed files queued again", onDone: game.refresh });
  const selection = useGameSelection(() => {
    game.refresh();
    refreshStatus();
  });
  const selectedOnly = selectsGames(status?.library.download_mode);
  const options = useGameOptions(id, game.refresh);
  // A game can opt in to what the library-wide settings leave out.
  const canOptInstallers = status ? !status.library.include_installers : false;
  const canOptDLC = status ? !status.library.include_dlc : false;
  const canOptExtras = status ? !status.library.include_extras : false;
  const canOptSaves = status ? !status.library.include_saves : false;
  // DESIGN MOCK: artwork is not a setting yet, so the opt-in is always offered and only lives on this page.
  const canOptArtwork = true;
  const [artworkOpt, setArtworkOpt] = useState(false);
  const gameOptions = (g: GameSummary): GameOptionsValue => ({
    include_installers: g.include_installers,
    include_dlc: g.include_dlc,
    include_extras: g.include_extras,
    include_saves: g.include_saves,
  });

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
  const hasErrors = products.some((p) => p.files.some((f) => f.status === "error"));
  const hasFiles = products.some((p) => p.files.length > 0);
  const sorted = [...products].sort((a, b) => Number(a.is_dlc) - Number(b.is_dlc) || a.title.localeCompare(b.title));

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

  return (
    <div className="stack">
      <div>{back}</div>
      <ApiErrorNotice error={game.error} stale />
      {selection.modal}
      {options.modal}

      <div className="game-head">
        <div className="game-cover">
          <CoverImage src={g.image} />
        </div>
        <div className="game-info">
          <h1 className="page-title">{g.title}</h1>
          <div className="muted">
            <GameStatusDot status={g.status} /> · {meta.join(" · ")}
          </div>
          <div className="mono path">{g.folder || "—"}</div>
          <TagList tags={g.tags} />
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
              <button className="btn" onClick={() => void sync.run()} disabled={sync.pending}>
                {sync.pending && <Spinner />} Re-check on GOG
              </button>
            )}
            {hasErrors && (
              <button className="btn danger" onClick={() => void retry.run()} disabled={retry.pending}>
                {retry.pending && <Spinner />} Retry failed
              </button>
            )}
          </div>
        </div>
      </div>

      {(canOptInstallers || canOptDLC || canOptExtras || canOptSaves || canOptArtwork) && (
        <div className="check-list game-options" role="group" aria-label="Extra content for this game">
          {canOptInstallers && (
            <label className="check">
              <input
                type="checkbox"
                checked={g.include_installers}
                disabled={options.busy}
                onChange={(e) => void options.setOptions({ ...gameOptions(g), include_installers: e.target.checked })}
              />
              <span>
                Also download the base game installers for this game
                <span className="desc">
                  The library settings leave the games themselves out; this one is fetched anyway.
                </span>
              </span>
            </label>
          )}
          {canOptDLC && (
            <label className="check">
              <input
                type="checkbox"
                checked={g.include_dlc}
                disabled={options.busy}
                onChange={(e) => void options.setOptions({ ...gameOptions(g), include_dlc: e.target.checked })}
              />
              <span>
                Also download DLC for this game
                <span className="desc">The library settings leave DLC out; this game gets it anyway.</span>
              </span>
            </label>
          )}
          {canOptExtras && (
            <label className="check">
              <input
                type="checkbox"
                checked={g.include_extras}
                disabled={options.busy}
                onChange={(e) => void options.setOptions({ ...gameOptions(g), include_extras: e.target.checked })}
              />
              <span>
                Also download extras for this game
                <span className="desc">Soundtracks, manuals, wallpapers and the like, for this game only.</span>
              </span>
            </label>
          )}
          {canOptSaves && (
            <label className="check">
              <input
                type="checkbox"
                checked={g.include_saves}
                disabled={options.busy}
                onChange={(e) => void options.setOptions({ ...gameOptions(g), include_saves: e.target.checked })}
              />
              <span>
                Also back up cloud saves for this game
                <span className="desc">
                  The save games GOG Galaxy keeps in the cloud, if this game has any, for this game only.
                </span>
              </span>
            </label>
          )}
          {canOptArtwork && (
            <label className="check">
              <input
                type="checkbox"
                checked={artworkOpt}
                disabled={options.busy}
                onChange={(e) => setArtworkOpt(e.target.checked)}
              />
              <span>
                Also download artwork for this game
                <span className="desc">
                  The library settings leave artwork out; this game's cover, background, logo, icon and screenshots are
                  fetched anyway.
                </span>
              </span>
            </label>
          )}
        </div>
      )}

      {unselected && (
        <>
          <EmptyState>
            This game is not selected for download. Select it to fetch its installers
            {hasFiles ? "; files kept from before are listed below as inactive." : "."}
          </EmptyState>
          <OfferSection offer={offer} game={g} />
        </>
      )}
      {/* An unselected game with nothing on disk has said all there is to say above. */}
      {(!unselected || hasFiles) &&
        (sorted.length === 0 ? (
          <EmptyState>
            {g.status === "unsynced"
              ? "Details for this game have not been fetched yet. Use “Re-check on GOG” to fetch them now."
              : "GOG offers no files for this game that match the chosen platforms and languages."}
          </EmptyState>
        ) : (
          sorted.map((p) => <ProductSection key={p.id} product={p} onChanged={game.refresh} />)
        ))}
      {!unselected && g.status !== "unsynced" && <ArtworkSection game={g} />}
    </div>
  );
}

/** What GOG offers for a game that is not selected, with what the settings would pick marked. */
function OfferSection({ offer, game }: { offer: PollingState<Offer>; game: GameSummary }) {
  if (offer.loading && !offer.data) return <Loading text="Asking GOG what is available…" />;
  if (!offer.data) return <ApiErrorNotice error={offer.error} />;
  const { products } = offer.data;
  // DESIGN MOCK: the artwork rows are made up here, so they are added to the
  // base game's items and to the totals here too.
  const artwork = mockArtworkOffer(game);
  const wanted_files = offer.data.wanted_files + artwork.reduce((n, it) => n + it.files, 0);
  const wanted_bytes = offer.data.wanted_bytes + artwork.reduce((n, it) => n + it.size, 0);
  const sorted = [...products]
    .sort((a, b) => Number(a.is_dlc) - Number(b.is_dlc) || a.title.localeCompare(b.title))
    .map((p) => (p.is_dlc ? p : { ...p, items: [...p.items, ...artwork] }));
  return (
    <>
      <div className="muted">
        Available on GOG. With the current settings, selecting this game downloads{" "}
        {wanted_files === 0 ? "nothing" : `${plural(wanted_files, "file")} (${formatBytes(wanted_bytes)})`}; the files
        marked <IconCheck /> are the ones it would fetch.
      </div>
      {sorted.map((p) => (
        <OfferProductSection key={p.id} product={p} />
      ))}
    </>
  );
}

function OfferProductSection({ product }: { product: OfferProduct }) {
  const wanted = product.items.filter((i) => i.wanted).length;
  return (
    <section className="section product">
      <div className="section-head">
        <h2 className="section-title">
          {product.title}
          {product.is_dlc && <span className="dlc">DLC</span>}
        </h2>
        <span className="meta num">
          {wanted} of {product.items.length} items
        </span>
      </div>
      {product.items.length === 0 ? (
        <EmptyState>GOG offers no files for this product.</EmptyState>
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
              <th className="num">Files</th>
              <th>Selected</th>
            </tr>
          </thead>
          <tbody>
            {product.items.map((it, i) => (
              <OfferRow key={i} item={it} />
            ))}
          </tbody>
        </DataTable>
      )}
    </section>
  );
}

function OfferRow({ item: it }: { item: OfferItem }) {
  const kind = it.kind === "installer" ? "Installer" : it.kind === "artwork" ? "Artwork" : "Extra";
  return (
    <tr className={it.wanted ? "" : "dim"}>
      <td>{it.type ? `${kind} · ${it.type}` : kind}</td>
      <td>{it.os ? platformLabel(it.os) : "—"}</td>
      <td className="mono">{it.language || "—"}</td>
      <td className="clip" title={it.name}>
        {it.name}
      </td>
      <td className="mono">{it.version || "—"}</td>
      <td className="num">{formatBytes(it.size)}</td>
      <td className="num">{it.files}</td>
      <td>{it.wanted ? <IconCheck /> : <span className="faint">—</span>}</td>
    </tr>
  );
}

function ProductSection({ product, onChanged }: { product: Product; onChanged: () => void }) {
  const done = product.files.filter((f) => f.status === "done").length;
  const active = product.files.filter((f) => f.status !== "inactive").length;
  const inactive = product.files.length - active;
  // Files kept from before are listed but not wanted; a product that only has
  // those must not read as "0 of 0 files" above a table full of them.
  let count = `${done} of ${active} files`;
  if (active === 0 && inactive > 0) count = plural(inactive, "inactive file");
  else if (inactive > 0) count += ` · ${inactive} inactive`;
  return (
    <section className="section product">
      <div className="section-head">
        <h2 className="section-title">
          {product.title}
          {product.is_dlc && <span className="dlc">DLC</span>}
        </h2>
        <span className="meta num">{count}</span>
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

const FILE_KIND_LABELS: Record<string, string> = {
  installer: "Installer",
  extra: "Extra",
  save: "Cloud save",
  artwork: "Artwork",
};

function FileRow({ file: f, onChanged }: { file: GameFile; onChanged: () => void }) {
  const retry = useToastAction(() => retryFile(f.id), { success: "File queued again", onDone: onChanged });
  const downloading = f.status === "downloading";
  const isError = f.status === "error";
  const dl = f.progress?.downloaded_bytes ?? 0;
  return (
    <tr className={isError ? "error" : f.status === "inactive" ? "dim" : ""}>
      <td>{FILE_KIND_LABELS[f.kind] ?? f.kind}</td>
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
            <button className="link-btn" onClick={() => void retry.run()} disabled={retry.pending}>
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
      <td className="muted">
        {f.downloaded_at ? <TimeAgo iso={f.downloaded_at} /> : <span className="faint">—</span>}
      </td>
    </tr>
  );
}
