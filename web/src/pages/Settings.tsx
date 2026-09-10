import { useEffect, useState } from "react";
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
import { ApiErrorNotice, CheckCard, Loading, Spinner, Switch } from "../components/Common";
import { IconUser } from "../components/Icons";
import {
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
  const n = Math.round(Number(v));
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, n));
}

export function SettingsPage() {
  const settingsState = useAsync(getSettings);
  const languagesState = useAsync(getLanguages);
  const languages = languagesState.data?.languages ?? [];

  if (!settingsState.data) {
    return (
      <div className="stack">
        <div className="page-head">
          <h1>Settings</h1>
        </div>
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
    <div className="stack">
      <div className="page-head">
        <h1>Settings</h1>
      </div>
      <SettingsForm
        key={JSON.stringify(settingsState.data)}
        initial={settingsState.data}
        languages={languages}
        onSaved={settingsState.refresh}
      />
      <AccountCard />
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
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<SettingsPreview | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const n = Number(mbps);
    if (Number.isFinite(n) && n >= 0) {
      setForm((f) => ({ ...f, speed_limit_kbps: mbpsToKbps(n) }));
    }
  }, [mbps]);

  const set = <K extends keyof Settings>(key: K, value: Settings[K]) => setForm((f) => ({ ...f, [key]: value }));

  const dirty = JSON.stringify(form) !== JSON.stringify(initial);
  const valid =
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
      await putSettings(form, onRemoved);
      setConfirm(null);
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
      const preview = await previewSettings(form);
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

  return (
    <>
      <div className="card">
        <div className="form-section">
          <h3>Platforms</h3>
          <PlatformPicker value={form.platforms} onChange={(v) => set("platforms", v)} disabled={busy} />
          {form.platforms.length === 0 && <span className="err-text small">Pick at least one platform.</span>}
        </div>
        <div className="divider" style={{ margin: "16px 0" }} />
        <div className="form-section">
          <h3>Languages</h3>
          {languages.length === 0 && <Loading text="Loading languages…" />}
          <LanguagePicker
            options={languages}
            value={form.languages}
            onChange={(v) => set("languages", v)}
            disabled={busy}
          />
          {form.languages.length === 0 && <span className="err-text small">Pick at least one language.</span>}
          <LanguageFallbackSwitch value={form.language_fallback} onChange={(v) => set("language_fallback", v)} disabled={busy} />
        </div>
        <div className="divider" style={{ margin: "16px 0" }} />
        <div className="form-section">
          <h3>Content</h3>
          <div className="check-row">
            <CheckCard
              checked={form.include_dlc}
              onChange={(v) => set("include_dlc", v)}
              label="Include DLC"
              hint="Installers for downloadable content you own."
              disabled={busy}
            />
            <CheckCard
              checked={form.include_extras}
              onChange={(v) => set("include_extras", v)}
              label="Include extras"
              hint="Soundtracks, manuals, wallpapers and other bonus content."
              disabled={busy}
            />
          </div>
        </div>
        <div className="divider" style={{ margin: "16px 0" }} />
        <div className="form-section">
          <h3>Downloads</h3>
          <div className="form-grid">
            <div className="field">
              <label htmlFor="s-conc">Max concurrent downloads</label>
              <div className="input-group">
                <input
                  id="s-conc"
                  className="input"
                  type="number"
                  min={1}
                  max={8}
                  step={1}
                  value={form.max_concurrent_downloads}
                  onChange={(e) => set("max_concurrent_downloads", clampInt(e.target.value, 1, 8, 1))}
                  disabled={busy}
                />
                <span className="suffix">1–8</span>
              </div>
            </div>
            <div className="field">
              <label htmlFor="s-speed">Speed limit</label>
              <div className="input-group">
                <input
                  id="s-speed"
                  className="input"
                  type="number"
                  min={0}
                  step={0.1}
                  value={mbps}
                  onChange={(e) => setMbps(e.target.value)}
                  onBlur={() => setMbps(String(kbpsToMbps(form.speed_limit_kbps)))}
                  disabled={busy}
                />
                <span className="suffix">MB/s</span>
              </div>
              <span className="hint">0 = unlimited. Applies to all downloads together.</span>
            </div>
            <div className="field">
              <label htmlFor="s-interval">Check GOG every</label>
              <div className="input-group">
                <input
                  id="s-interval"
                  className="input"
                  type="number"
                  min={1}
                  max={168}
                  step={1}
                  value={form.check_interval_hours}
                  onChange={(e) => set("check_interval_hours", clampInt(e.target.value, 1, 168, 6))}
                  disabled={busy}
                />
                <span className="suffix">hours</span>
              </div>
              <span className="hint">1–168 hours between library syncs.</span>
            </div>
          </div>
          <Switch
            checked={form.downloads_paused}
            onChange={(v) => set("downloads_paused", v)}
            disabled={busy}
            label="Downloads paused"
          />
        </div>
        <div className="form-actions" style={{ marginTop: 16 }}>
          <button className="btn primary" onClick={save} disabled={busy || !dirty || !valid}>
            {busy && <Spinner />} Save changes
          </button>
          <button className="btn ghost" onClick={() => { setForm(initial); setMbps(String(kbpsToMbps(initial.speed_limit_kbps))); }} disabled={busy || !dirty}>
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
          onCancel={() => setConfirm(null)}
          onKeep={() => void apply("keep")}
          onDelete={() => void apply("delete")}
        />
      )}
    </>
  );
}

function AccountCard() {
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
    <div className="card">
      <div className="card-head">
        <h2>
          <IconUser /> GOG account
        </h2>
      </div>
      <div className="stack">
        {status?.authenticated && user ? (
          <div>
            Connected as <strong>{user.username}</strong>{" "}
            <span className="faint small">(id {user.id})</span>
          </div>
        ) : (
          <div className="err-text">
            Not connected{status?.auth_error ? `: ${status.auth_error}` : ""}.
          </div>
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
      </div>
    </div>
  );
}
