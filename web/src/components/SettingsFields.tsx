import type { LanguageOption, Platform, RemovedSummary } from "../api";
import { formatBytes, PLATFORM_LABELS, plural } from "../format";
import { CheckCard, Switch } from "./Common";
import { Modal } from "./Modal";

export const ALL_PLATFORMS: Platform[] = ["windows", "mac", "linux"];

export function PlatformPicker({
  value,
  onChange,
  disabled,
}: {
  value: Platform[];
  onChange: (v: Platform[]) => void;
  disabled?: boolean;
}) {
  const toggle = (p: Platform, on: boolean) => {
    const set = new Set(value);
    if (on) set.add(p);
    else set.delete(p);
    onChange(ALL_PLATFORMS.filter((x) => set.has(x)));
  };
  return (
    <div className="check-row">
      {ALL_PLATFORMS.map((p) => (
        <CheckCard
          key={p}
          checked={value.includes(p)}
          onChange={(on) => toggle(p, on)}
          label={PLATFORM_LABELS[p]}
          disabled={disabled}
        />
      ))}
    </div>
  );
}

export function LanguagePicker({
  options,
  value,
  onChange,
  disabled,
}: {
  options: LanguageOption[];
  value: string[];
  onChange: (v: string[]) => void;
  disabled?: boolean;
}) {
  // Include codes that are selected but not offered by the backend so they are not silently lost.
  const known = new Set(options.map((o) => o.code));
  const extra = value.filter((c) => !known.has(c)).map((code) => ({ code, name: code }));
  const all = [...options, ...extra];
  const toggle = (code: string) => {
    if (disabled) return;
    if (value.includes(code)) onChange(value.filter((c) => c !== code));
    else onChange([...value, code]);
  };
  if (all.length === 0) return <span className="muted">No languages available.</span>;
  return (
    <div className="chip-list" role="group" aria-label="Languages">
      {all.map((o) => {
        const on = value.includes(o.code);
        return (
          <label key={o.code} className={`chip ${on ? "on" : ""}`}>
            <input type="checkbox" checked={on} disabled={disabled} onChange={() => toggle(o.code)} />
            {o.name} <span className="count">{o.code}</span>
          </label>
        );
      })}
    </div>
  );
}

export function LanguageFallbackSwitch({
  value,
  onChange,
  disabled,
}: {
  value: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <Switch
      checked={value}
      onChange={onChange}
      disabled={disabled}
      label={
        <span>
          Fall back to another language{" "}
          <span className="muted small">— if none of the chosen languages exist for a game, download what GOG offers</span>
        </span>
      }
    />
  );
}

/** Turns a preview "reason" like "platform:linux" into readable text. */
export function describeReason(reason: string, languages: LanguageOption[]): string {
  const [kind, arg] = reason.split(":", 2);
  switch (kind) {
    case "platform":
      return `${PLATFORM_LABELS[arg ?? ""] ?? arg} installers are no longer wanted`;
    case "language": {
      const name = languages.find((l) => l.code === arg)?.name ?? arg;
      return `Language "${name}" is no longer selected`;
    }
    case "dlc":
      return "DLC is no longer included";
    case "extras":
      return "Extras are no longer included";
    default:
      return reason;
  }
}

export function RemovalConfirmModal({
  removed,
  reasons,
  languages,
  busy,
  onKeep,
  onDelete,
  onCancel,
}: {
  removed: RemovedSummary;
  reasons: string[];
  languages: LanguageOption[];
  busy?: boolean;
  onKeep: () => void;
  onDelete: () => void;
  onCancel: () => void;
}) {
  return (
    <Modal
      title="Some files will no longer be tracked"
      onClose={onCancel}
      footer={
        <>
          <button className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button className="btn primary" onClick={onKeep} disabled={busy}>
            Keep files on disk
          </button>
          <button className="btn danger" onClick={onDelete} disabled={busy}>
            Delete files from disk
          </button>
        </>
      }
    >
      <p>
        With these settings <strong>{plural(removed.files, "file")}</strong> ({formatBytes(removed.bytes)}) that are
        currently tracked will no longer be wanted. Of those,{" "}
        <strong>{plural(removed.downloaded_files, "file")}</strong> ({formatBytes(removed.downloaded_bytes)}) are
        already downloaded.
      </p>
      {reasons.length > 0 && (
        <div>
          <div className="muted small" style={{ marginBottom: 4 }}>
            Because:
          </div>
          <ul>
            {reasons.map((r) => (
              <li key={r}>{describeReason(r, languages)}</li>
            ))}
          </ul>
        </div>
      )}
      <p className="muted small">
        <strong>Keep</strong> leaves the files where they are and marks them inactive in the library.{" "}
        <strong>Delete</strong> removes them from disk and from the game's file list. Either way they will not be
        downloaded again unless you change the settings back.
      </p>
    </Modal>
  );
}
