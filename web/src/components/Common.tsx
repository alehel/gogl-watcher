import type { ReactNode } from "react";
import { errorMessage, NetworkError } from "../api";
import { formatAbsolute, formatRelative } from "../format";
import { useNow } from "../hooks";
import { IconAlert, IconBox } from "./Icons";

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

export function EmptyState({
  title,
  children,
  icon,
  compact,
}: {
  title: string;
  children?: ReactNode;
  icon?: ReactNode;
  compact?: boolean;
}) {
  return (
    <div className={`empty ${compact ? "compact" : ""}`}>
      {icon ?? <IconBox />}
      <div className="title">{title}</div>
      {children && <div>{children}</div>}
    </div>
  );
}

/** Inline notice for a polling error; says "API unreachable" for network failures. */
export function ApiErrorNotice({ error, stale }: { error: unknown; stale?: boolean }) {
  if (!error) return null;
  const network = error instanceof NetworkError;
  return (
    <div className="notice error" role="alert">
      <IconAlert />
      <span>
        {network ? "API unreachable — retrying…" : `Request failed: ${errorMessage(error)}`}
        {stale && network ? " Showing the last known data." : ""}
      </span>
    </div>
  );
}

/** Relative time with the absolute time in a title attribute. */
export function TimeAgo({ iso, prefix }: { iso: string | null | undefined; prefix?: string }) {
  const now = useNow();
  if (!iso) return <span className="muted">never</span>;
  return (
    <span title={formatAbsolute(iso)}>
      {prefix}
      {formatRelative(iso, now)}
    </span>
  );
}

export function Switch({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className="switch">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  );
}

export function CheckCard({
  checked,
  onChange,
  label,
  hint,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
  hint?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className={`check ${checked ? "checked" : ""}`}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="check-text">
        <span>{label}</span>
        {hint && <span className="hint">{hint}</span>}
      </span>
    </label>
  );
}
