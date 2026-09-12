import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  errorMessage,
  getLanguages,
  getSettings,
  logout,
  previewFromError,
  previewSettings,
  putSettings,
  type OnRemoved,
  type Settings,
  type SettingsPreview,
} from "../api";
import { ApiErrorNotice, Checkbox, Loading, Spinner } from "../components/Common";
import { EstimateNote, useEstimatePair } from "../components/Estimate";
import {
  DownloadModePicker,
  LanguageFallbackSwitch,
  LanguagePicker,
  NumberField,
  PlatformPicker,
  RemovalConfirmModal,
} from "../components/SettingsFields";
import { useStatus } from "../components/StatusContext";
import { useToast, useToastAction } from "../components/Toast";
import { formatBytes, kbpsToMbps, mbpsToKbps } from "../format";
import { useAsync } from "../hooks";

// The bounds the backend enforces; the form refuses to send anything outside them.
const CONCURRENCY = { min: 1, max: 8 };
const INTERVAL_HOURS = { min: 1, max: 168 };

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
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<SettingsPreview | null>(null);
  // What will be sent: the user's edits applied to the settings as stored when saving started.
  const [payload, setPayload] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);

  const set = <K extends keyof Settings>(key: K, value: Settings[K]) => setForm((f) => ({ ...f, [key]: value }));

  const dirty = JSON.stringify(form) !== JSON.stringify(initial);
  const valid =
    form.download_mode !== "" &&
    form.platforms.length > 0 &&
    form.languages.length > 0 &&
    form.max_concurrent_downloads >= CONCURRENCY.min &&
    form.max_concurrent_downloads <= CONCURRENCY.max &&
    form.check_interval_hours >= INTERVAL_HOURS.min &&
    form.check_interval_hours <= INTERVAL_HOURS.max &&
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
      const preview = previewFromError(e);
      if (preview) {
        setConfirm(preview);
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

  const reset = () => setForm(initial);

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
        <StorageSection form={form} initial={initial} />
        <section className="form-section">
          <h2>Downloads</h2>
          <div className="form-row">
            <NumberField
              id="s-conc"
              label="Concurrent downloads"
              unit={`${CONCURRENCY.min}–${CONCURRENCY.max}`}
              value={form.max_concurrent_downloads}
              onChange={(v) => set("max_concurrent_downloads", v)}
              min={CONCURRENCY.min}
              max={CONCURRENCY.max}
              disabled={busy}
            />
            <NumberField
              id="s-speed"
              label="Speed limit"
              unit="MB/s"
              hint="0 = unlimited, for all downloads together."
              // The stored value is only rewritten when the user edits the field: the
              // MB/s display is rounded, so echoing it back would change a limit that
              // was not set through this UI.
              value={kbpsToMbps(form.speed_limit_kbps)}
              onChange={(v) => set("speed_limit_kbps", mbpsToKbps(v))}
              min={0}
              step={0.1}
              disabled={busy}
            />
            <NumberField
              id="s-interval"
              label="Check GOG every"
              unit="hours"
              hint={`${INTERVAL_HOURS.min}–${INTERVAL_HOURS.max} hours between library syncs.`}
              value={form.check_interval_hours}
              onChange={(v) => set("check_interval_hours", v)}
              min={INTERVAL_HOURS.min}
              max={INTERVAL_HOURS.max}
              disabled={busy}
            />
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

/**
 * What the choices above would cost. The saved settings are costed alongside
 * the edited ones, so the page can answer the question that actually gets
 * asked: not "how big is my library" but "how much would this change add".
 */
function StorageSection({ form, initial }: { form: Settings; initial: Settings }) {
  const { status } = useStatus();
  const { data, loading } = useEstimatePair(form, initial);
  const free = status?.disk.free_bytes ?? 0;
  const estimate = data?.edited ?? null;
  const delta = data ? data.edited.bytes - data.saved.bytes : 0;

  return (
    <section className="form-section">
      <h2>Storage</h2>
      {loading && !estimate ? <Loading text="Measuring…" /> : <EstimateNote estimate={estimate} className="hint" />}
      <span className="hint">
        {delta !== 0 && (
          <>
            <strong>
              {delta > 0 ? "+" : "−"}
              {formatBytes(Math.abs(delta))}
            </strong>{" "}
            against your saved settings ·{" "}
          </>
        )}
        {formatBytes(free)} free on disk
        {estimate && estimate.bytes > free && free > 0 ? " — a full backup would not fit" : ""}
      </span>
    </section>
  );
}

function AccountSection() {
  const { status, refresh } = useStatus();
  const navigate = useNavigate();
  const logoutAction = useToastAction(logout, {
    success: "Disconnected from GOG",
    onDone: () => {
      refresh();
      navigate("/auth");
    },
  });

  const disconnect = () => {
    if (!window.confirm("Disconnect the GOG account? Syncing stops until you authorize again.")) return;
    void logoutAction.run();
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
          <button className="btn danger" onClick={disconnect} disabled={logoutAction.pending}>
            {logoutAction.pending && <Spinner />} Disconnect
          </button>
        )}
      </div>
    </section>
  );
}
