import type { ReactNode } from "react";
import { errorMessage, NetworkError } from "../api";
import { formatAbsolute, formatRelative } from "../format";
import { useNow } from "../hooks";

export function Spinner() {
  return <span className="spinner" aria-hidden="true" />;
}

export function Loading({ text = "Loading…" }: { text?: string }) {
  return (
    <div className="loading">
      <Spinner />
      <span>{text}</span>
    </div>
  );
}

/** One sentence in --text-2, optionally with a link; no icon, no heading. */
export function EmptyState({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>;
}

/** Inline strip for a polling error; says "API unreachable" for network failures. */
export function ApiErrorNotice({ error, stale }: { error: unknown; stale?: boolean }) {
  if (!error) return null;
  const network = error instanceof NetworkError;
  return (
    <div className={`strip ${network ? "warn" : "danger"}`} role="alert">
      {network ? "API unreachable — retrying." : `Request failed: ${errorMessage(error)}`}
      {stale && network ? " Showing the last known data." : ""}
    </div>
  );
}

/** Relative time with the absolute time in a title attribute. */
export function TimeAgo({ iso, prefix }: { iso: string | null | undefined; prefix?: string }) {
  const now = useNow();
  if (!iso) return <span className="faint">never</span>;
  return (
    <span title={formatAbsolute(iso)}>
      {prefix}
      {formatRelative(iso, now)}
    </span>
  );
}

/** Plain checkbox row with an optional one-line description. */
export function Checkbox({
  checked,
  onChange,
  label,
  desc,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
  desc?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className="check">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>
        {label}
        {desc && <span className="desc">{desc}</span>}
      </span>
    </label>
  );
}
