import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  ApiError,
  errorMessage,
  getLanguages,
  getSettings,
  logout,
  previewSettings,
  putSettings,
  type OnRemoved,
  type Settings,
  type SettingsPreview,
} from "../api";
import { ApiErrorNotice, Checkbox, Loading, Spinner } from "../components/Common";
import {
  DownloadModePicker,
  LanguageFallbackSwitch,
  LanguagePicker,
  PlatformPicker,
  RemovalConfirmModal,
} from "../components/SettingsFields";
import { useStatus } from "../components/StatusContext";
import { useToast } from "../components/Toast";
import { kbpsToMbps, mbpsToKbps } from "../format";
import { useAsync } from "../hooks";

function clampInt(v: string, min: number, max: number, fallback: number): number {
  if (v.trim() === "") return fallback;
  const n = Math.round(Number(v));
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, n));
}

/** Only the fields the user changed, on top of the settings as stored right now. */
function mergeEdits(fresh: Settings, form: Settings, initial: Settings): Settings {
  const out = { ...fresh };
  for (const key of Object.keys(form) as Array<keyof Settings>) {
    if (JSON.stringify(form[key]) !== JSON.stringify(initial[key])) {
      (out as Record<string, unknown>)[key] = form[key];
    }
  }
  return out;
}

export function SettingsPage() {
  const settingsState = useAsync(getSettings);
  const languagesState = useAsync(getLanguages);
  const languages = languagesState.data?.languages ?? [];

  if (!settingsState.data) {
    return (
      <div className="stack settings">
        <h1 className="page-title">Settings</h1>
        {settingsState.error ? (
          <>
            <ApiErrorNotice error={settingsState.error} />
            <div>
              <button className="btn" onClick={settingsState.refresh}>
                Retry
              </button>
            </div>
          </>
        ) : (
          <Loading text="Loading settings…" />
        )}
      </div>
    );
  }

  return (
    <div className="stack settings">
      <h1 className="page-title">Settings</h1>
      <SettingsForm
        key={JSON.stringify(settingsState.data)}
        initial={settingsState.data}
        languages={languages}
        onSaved={settingsState.refresh}
      />
      <AccountSection />
    </div>
  );
}

function SettingsForm({
  initial,
  languages,
  onSaved,
}: {
  initial: Settings;
  languages: Parameters<typeof LanguagePicker>[0]["options"];
  onSaved: () => void;
}) {
  const toast = useToast();
  const [form, setForm] = useState<Settings>(initial);
  const [mbps, setMbps] = useState(String(kbpsToMbps(initial.speed_limit_kbps)));
  // Integer fields keep their raw text while being edited so they can be cleared and retyped.
  const [concText, setConcText] = useState(String(initial.max_concurrent_downloads));
  const [intervalText, setIntervalText] = useState(String(initial.check_interval_hours));
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<SettingsPreview | null>(null);
  // What will be sent: the user's edits applied to the settings as stored when saving started.
  const [payload, setPayload] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);

  const set = <K extends keyof Settings>(key: K, value: Settings[K]) => setForm((f) => ({ ...f, [key]: value }));

  // The stored value is only rewritten when the user edits the field: the MB/s
  // display is rounded, so echoing it back on mount would make an untouched form
  // dirty (and change the stored limit) whenever the value was not set via this UI.
  const setSpeed = (value: string) => {
    setMbps(value);
    const n = Number(value);
    if (value.trim() !== "" && Number.isFinite(n) && n >= 0) set("speed_limit_kbps", mbpsToKbps(n));
  };

  const dirty = JSON.stringify(form) !== JSON.stringify(initial);
  const valid =
    form.download_mode !== "" &&
    form.platforms.length > 0 &&
    form.languages.length > 0 &&
    form.max_concurrent_downloads >= 1 &&
    form.max_concurrent_downloads <= 8 &&
    form.check_interval_hours >= 1 &&
    form.check_interval_hours <= 168 &&
    form.speed_limit_kbps >= 0;

  const apply = async (onRemoved: OnRemoved) => {
    setBusy(true);
    setError(null);
    try {
      await putSettings(payload ?? form, onRemoved);
      setConfirm(null);
      setPayload(null);
      toast.success("Settings saved");
      onSaved();
    } catch (e) {
      if (e instanceof ApiError && e.confirmationRequired) {
        const body = e.body as Partial<SettingsPreview>;
        setConfirm({
          needs_confirmation: true,
          removed: body.removed ?? { files: 0, bytes: 0, downloaded_files: 0, downloaded_bytes: 0 },
          reasons: body.reasons ?? [],
        });
      } else {
        setConfirm(null);
        setError(errorMessage(e));
        toast.error(`Could not save settings: ${errorMessage(e)}`);
      }
    } finally {
      setBusy(false);
    }
  };

  const save = async () => {
    if (!valid) return;
    setBusy(true);
    setError(null);
    try {
      // Another page or device may have changed settings (e.g. paused downloads)
      // since this form was loaded; only send the fields edited here.
      const merged = mergeEdits(await getSettings(), form, initial);
      setPayload(merged);
      const preview = await previewSettings(merged);
      if (preview.needs_confirmation) {
        setConfirm(preview);
        setBusy(false);
        return;
      }
    } catch (e) {
      setBusy(false);
      setError(errorMessage(e));
      toast.error(`Could not check settings: ${errorMessage(e)}`);
      return;
    }
    await apply(null);
  };

  const reset = () => {
    setForm(initial);
    setMbps(String(kbpsToMbps(initial.speed_limit_kbps)));
    setConcText(String(initial.max_concurrent_downloads));
    setIntervalText(String(initial.check_interval_hours));
  };

  return (
    <>
      <div>
        <section className="form-section">
          <h2>Games</h2>
          <DownloadModePicker value={form.download_mode} onChange={(v) => set("download_mode", v)} disabled={busy} />
          {form.download_mode === "selected" && (
            <span className="hint">
              Pick games in the <Link to="/library">library</Link>. Switching to this mode keeps only selected games;
              you will be asked what to do with files of games that are not selected.
            </span>
          )}
        </section>
        <section className="form-section">
          <h2>Platforms</h2>
          <PlatformPicker value={form.platforms} onChange={(v) => set("platforms", v)} disabled={busy} />
          {form.platforms.length === 0 && <span className="err-text small">Pick at least one platform.</span>}
        </section>
        <section className="form-section">
          <h2>Languages</h2>
          {languages.length === 0 && <Loading text="Loading languages…" />}
          <LanguagePicker
            options={languages}
            value={form.languages}
            onChange={(v) => set("languages", v)}
            disabled={busy}
          />
          {form.languages.length === 0 && <span className="err-text small">Pick at least one language.</span>}
          <LanguageFallbackSwitch
            value={form.language_fallback}
            onChange={(v) => set("language_fallback", v)}
            disabled={busy}
          />
        </section>
        <section className="form-section">
          <h2>Content</h2>
          <div className="check-list">
            <Checkbox
              checked={form.include_dlc}
              onChange={(v) => set("include_dlc", v)}
              label="Include DLC"
              desc="Installers for downloadable content you own."
              disabled={busy}
            />
            <Checkbox
              checked={form.include_extras}
              onChange={(v) => set("include_extras", v)}
              label="Include extras"
              desc="Soundtracks, manuals, wallpapers and other bonus content."
              disabled={busy}
            />
          </div>
        </section>
        <section className="form-section">
          <h2>Downloads</h2>
          <div className="form-row">
            <div className="field">
              <label htmlFor="s-conc">Concurrent downloads</label>
              <div className="input-unit">
                <input
                  id="s-conc"
                  className="input num"
                  type="number"
                  min={1}
                  max={8}
                  step={1}
                  value={concText}
                  onChange={(e) => {
                    setConcText(e.target.value);
                    if (e.target.value.trim() !== "") set("max_concurrent_downloads", clampInt(e.target.value, 1, 8, 1));
                  }}
                  onBlur={() => setConcText(String(form.max_concurrent_downloads))}
                  disabled={busy}
                />
                <span className="unit">1–8</span>
              </div>
            </div>
            <div className="field">
              <label htmlFor="s-speed">Speed limit</label>
              <div className="input-unit">
                <input
                  id="s-speed"
                  className="input num"
                  type="number"
                  min={0}
                  step={0.1}
                  value={mbps}
                  onChange={(e) => setSpeed(e.target.value)}
                  onBlur={() => setMbps(String(kbpsToMbps(form.speed_limit_kbps)))}
                  disabled={busy}
                />
                <span className="unit">MB/s</span>
              </div>
              <span className="hint">0 = unlimited, for all downloads together.</span>
            </div>
            <div className="field">
              <label htmlFor="s-interval">Check GOG every</label>
              <div className="input-unit">
                <input
                  id="s-interval"
                  className="input num"
                  type="number"
                  min={1}
                  max={168}
                  step={1}
                  value={intervalText}
                  onChange={(e) => {
                    setIntervalText(e.target.value);
                    if (e.target.value.trim() !== "") set("check_interval_hours", clampInt(e.target.value, 1, 168, 6));
                  }}
                  onBlur={() => setIntervalText(String(form.check_interval_hours))}
                  disabled={busy}
                />
                <span className="unit">hours</span>
              </div>
              <span className="hint">1–168 hours between library syncs.</span>
            </div>
          </div>
          <Checkbox
            checked={form.downloads_paused}
            onChange={(v) => set("downloads_paused", v)}
            disabled={busy}
            label="Downloads paused"
          />
        </section>
        <div className="form-foot">
          <button className="btn primary" onClick={save} disabled={busy || !dirty || !valid}>
            {busy && <Spinner />} Save
          </button>
          <button className="btn text" onClick={reset} disabled={busy || !dirty}>
            Reset
          </button>
          {error && <span className="err-text small">{error}</span>}
          {!dirty && !error && <span className="faint small">No unsaved changes</span>}
        </div>
      </div>

      {confirm && (
        <RemovalConfirmModal
          removed={confirm.removed}
          reasons={confirm.reasons}
          languages={languages}
          busy={busy}
          onCancel={() => {
            setConfirm(null);
            setPayload(null);
          }}
          onKeep={() => void apply("keep")}
          onDelete={() => void apply("delete")}
        />
      )}
    </>
  );
}

function AccountSection() {
  const { status, refresh } = useStatus();
  const toast = useToast();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);

  const disconnect = async () => {
    if (!window.confirm("Disconnect the GOG account? Syncing stops until you authorize again.")) return;
    setBusy(true);
    try {
      await logout();
      toast.success("Disconnected from GOG");
      refresh();
      navigate("/auth");
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const user = status?.user ?? null;
  return (
    <section className="form-section">
      <h2>GOG account</h2>
      {status?.authenticated && user ? (
        <div>
          Connected as <strong>{user.username}</strong> <span className="faint small mono">id {user.id}</span>
        </div>
      ) : (
        <div className="err-text">Not connected{status?.auth_error ? `: ${status.auth_error}` : ""}.</div>
      )}
      <div className="btn-row">
        <Link to="/auth" className="btn">
          {status?.authenticated ? "Reconnect" : "Connect"}
        </Link>
        {status?.authenticated && (
          <button className="btn danger" onClick={disconnect} disabled={busy}>
            {busy && <Spinner />} Disconnect
          </button>
        )}
      </div>
    </section>
  );
}
