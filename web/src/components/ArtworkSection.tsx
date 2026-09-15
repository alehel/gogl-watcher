import type { GameSummary } from "../api";
import { formatBytes } from "../format";
import { mockArtwork, type ArtworkItem } from "../mock/artwork";
import { TimeAgo } from "./Common";
import { ProgressBar } from "./ProgressBar";
import { FileStatusDot } from "./StatusDot";

/**
 * DESIGN MOCK. The artwork GOG publishes for a game, as a gallery with the
 * state of each file, the way the product sections list installers. The items
 * are made up (see mock/artwork.ts); nothing is fetched or tracked.
 */
export function ArtworkSection({ game }: { game: GameSummary }) {
  const items = mockArtwork(game);
  const done = items.filter((i) => i.status === "done").length;
  const bytes = items.reduce((n, i) => n + i.size, 0);
  return (
    <section className="section artwork">
      <div className="section-head">
        <h2 className="section-title">Artwork</h2>
        <span className="meta num">
          {done} of {items.length} files · {formatBytes(bytes)}
        </span>
      </div>
      <p className="muted small">
        The cover, background, logo, icon and screenshots GOG publishes for the game, at full size, under{" "}
        <span className="mono">{game.folder}/artwork/</span>.
      </p>
      <div className="art-grid">
        {items.map((it) => (
          <ArtworkCard key={it.id} item={it} title={game.title} />
        ))}
      </div>
    </section>
  );
}

function ArtworkCard({ item: it, title }: { item: ArtworkItem; title: string }) {
  return (
    <figure className={`art ${it.kind}`}>
      <div className={`art-preview${it.format === "PNG" ? " checker" : ""}`}>
        <img src={it.preview} alt={`${it.label} of ${title}`} loading="lazy" />
        {it.status === "downloading" && (
          <ProgressBar line value={it.progress ?? 0} tone="info" label={`${it.label} download`} />
        )}
      </div>
      <figcaption>
        <div className="art-title" title={`GOG: ${it.source}`}>
          {it.label} <span className="faint">{it.format}</span>
        </div>
        <div className="art-meta num">
          {it.width} × {it.height} · {formatBytes(it.size)}
        </div>
        <div className="art-file mono clip" title={it.path}>
          {it.path}
        </div>
        <div className="art-status">
          <FileStatusDot status={it.status} />
          {it.downloaded_at && <TimeAgo iso={it.downloaded_at} />}
        </div>
      </figcaption>
    </figure>
  );
}
