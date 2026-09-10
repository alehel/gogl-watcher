import { formatPercent } from "../format";
import type { Tone } from "./StatusBadge";

interface Props {
  /** 0..1 */
  value: number;
  tone?: Tone;
  size?: "sm" | "md" | "lg";
  striped?: boolean;
  showPercent?: boolean;
  label?: string;
}

export function ProgressBar({ value, tone = "accent", size = "md", striped, showPercent, label }: Props) {
  const v = Number.isFinite(value) ? Math.max(0, Math.min(1, value)) : 0;
  const toneClass = tone === "accent" ? "" : tone;
  const bar = (
    <div
      className={`progress ${toneClass} ${size === "md" ? "" : size} ${striped ? "striped" : ""}`.trim()}
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(v * 100)}
      aria-label={label}
    >
      <span style={{ width: `${v * 100}%` }} />
    </div>
  );
  if (!showPercent) return bar;
  return (
    <div className="progress-row">
      {bar}
      <span className="pct">{formatPercent(v)}</span>
    </div>
  );
}
