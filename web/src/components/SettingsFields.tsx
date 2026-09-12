import { useState, type ReactNode } from "react";
import type { DownloadMode, LanguageOption, Platform, RemovedSummary } from "../api";
import { formatBytes, PLATFORM_LABELS, plural } from "../format";
import { Checkbox } from "./Common";
import { Modal } from "./Modal";

export const ALL_PLATFORMS: Platform[] = ["windows", "mac", "linux"];

/**
 * Number input with a unit. It keeps the text the user is typing, so the field
 * can be cleared and retyped, and only reports values that are within bounds;
 * leaving it puts the stored value back on screen.
 */
export function NumberField({
  id,
  label,
  unit,
  hint,
  value,
  onChange,
  min,
  max,
  step = 1,
  disabled,
}: {
  id: string;
  label: string;
  unit: string;
  hint?: ReactNode;
  value: number;
  onChange: (v: number) => void;
  min: number;
  max?: number;
  /** A step of 1 (the default) makes the field integer-only. */
  step?: number;
  disabled?: boolean;
}) {
  const [text, setText] = useState(String(value));
  const [lastValue, setLastValue] = useState(value);
  // Follow the value when it changes elsewhere (the form was reset or reloaded).
  if (value !== lastValue) {
    setLastValue(value);
    setText(String(value));
  }
  const edit = (raw: string) => {
    setText(raw);
    const n = Number(raw);
    if (raw.trim() === "" || !Number.isFinite(n)) return; // being cleared, not a value yet
    const rounded = step === 1 ? Math.round(n) : n;
    const bounded = Math.min(max ?? Infinity, Math.max(min, rounded));
    // Remember what we report so the value coming back does not overwrite what
    // is being typed: "12" in a 1-8 field stays until the field is left.
    setLastValue(bounded);
    onChange(bounded);
  };
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <div className="input-unit">
        <input
          id={id}
          className="input num"
          type="number"
          min={min}
          max={max}
          step={step}
          value={text}
          onChange={(e) => edit(e.target.value)}
          onBlur={() => setText(String(value))}
          disabled={disabled}
        />
        <span className="unit">{unit}</span>
      </div>
      {hint && <span className="hint">{hint}</span>}
    </div>
  );
}

export const DOWNLOAD_MODE_LABELS: Record<Exclude<DownloadMode, "">, string> = {
  all: "Every game I own",
  selected: "Only games I select",
};

/** Radio choice between downloading the whole library and only selected games. */
export function DownloadModePicker({
  value,
  onChange,
  disabled,
  name = "download_mode",
}: {
  value: DownloadMode;
  onChange: (v: Exclude<DownloadMode, "">) => void;
  disabled?: boolean;
  name?: string;
}) {
  const options: Array<{ value: Exclude<DownloadMode, "">; desc: string }> = [
    {
      value: "all",
      desc: "Everything in your library is downloaded and kept up to date. Large libraries need a lot of disk space.",
    },
    {
      value: "selected",
      desc: "Nothing is downloaded until you tick games in the library. New games you buy are listed but not downloaded.",
    },
  ];
  return (
    <div className="check-list" role="radiogroup" aria-label="Which games to download">
      {options.map((o) => (
        <label key={o.value} className="check">
          <input
            type="radio"
            name={name}
            value={o.value}
            checked={value === o.value}
            disabled={disabled}
            onChange={() => onChange(o.value)}
          />
          <span>
            {DOWNLOAD_MODE_LABELS[o.value]}
            <span className="desc">{o.desc}</span>
          </span>
        </label>
      ))}
    </div>
  );
}

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
    <div className="check-inline" role="group" aria-label="Platforms">
      {ALL_PLATFORMS.map((p) => (
        <Checkbox
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
    <div className="check-grid" role="group" aria-label="Languages">
      {all.map((o) => (
        <label key={o.code} className="check">
          <input type="checkbox" checked={value.includes(o.code)} disabled={disabled} onChange={() => toggle(o.code)} />
          <span className="clip">
            {o.name}
            <span className="mono code">{o.code}</span>
          </span>
        </label>
      ))}
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
    <Checkbox
      checked={value}
      onChange={onChange}
      disabled={disabled}
      label="Fall back to another language"
      desc="If none of the chosen languages exist for a game, download what GOG offers."
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
    case "unselected":
      return "The game is not selected for download";
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
          <button className="btn text" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button className="btn primary" onClick={onKeep} disabled={busy}>
            Keep files
          </button>
          <button className="btn danger" onClick={onDelete} disabled={busy}>
            Delete files
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
          <div className="small faint" style={{ marginBottom: 4 }}>
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
