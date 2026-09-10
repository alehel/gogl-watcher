import { formatPercent } from "../format";
import type { Tone } from "./StatusDot";

interface Props {
  /** 0..1 */
  value: number;
  tone?: Tone;
  /** 2px full-width line instead of the 3px bar. */
  line?: boolean;
  /** Renders the 96px bar with the percentage next to it. */
  showPercent?: boolean;
  label?: string;
}

export function ProgressBar({ value, tone, line, showPercent, label }: Props) {
  const v = Number.isFinite(value) ? Math.max(0, Math.min(1, value)) : 0;
  const bar = (
    <div
      className={`bar${line ? " line" : ""}${tone ? ` ${tone}` : ""}`}
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
    <div className="bar-row">
      {bar}
      <span className="pct">{formatPercent(v)}</span>
    </div>
  );
}
