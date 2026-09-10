import { Fragment, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  completeSetup,
  errorMessage,
  getLanguages,
  getSettings,
  putSettings,
  type LanguageOption,
  type Platform,
  type Settings,
  type SetupStep,
  type User,
} from "../api";
import { AuthorizeForm } from "../components/AuthorizeForm";
import { Loading, Spinner } from "../components/Common";
import { Brand } from "../components/Layout";
import {
  ALL_PLATFORMS,
  LanguageFallbackSwitch,
  LanguagePicker,
  PlatformPicker,
} from "../components/SettingsFields";
import { StatusDot } from "../components/StatusDot";
import { useStatus } from "../components/StatusContext";
import { useAsync } from "../hooks";
import { PLATFORM_LABELS } from "../format";

const STEPS = [
  { key: "auth", label: "Authorize" },
  { key: "platforms", label: "Platforms" },
  { key: "content", label: "Content" },
  { key: "done", label: "Finish" },
] as const;

function stepIndex(step: SetupStep): number {
  const i = STEPS.findIndex((s) => s.key === step);
  return i < 0 ? 0 : i;
}

export function SetupPage() {
  const { status, refresh } = useStatus();
  const navigate = useNavigate();
  // Furthest step the backend allows; the user may browse back to earlier ones.
  const furthest = stepIndex(status?.setup_step ?? "auth");
  const [active, setActive] = useState(furthest);
  const [lastFurthest, setLastFurthest] = useState(furthest);
  if (furthest !== lastFurthest) {
    // Backend moved on (e.g. after connecting) — follow it.
    setLastFurthest(furthest);
    setActive(furthest);
  }

  const settingsState = useAsync(getSettings);
  const languagesState = useAsync(getLanguages);
  const languages = languagesState.data?.languages ?? [];

  const goto = (i: number) => {
    if (i <= furthest) setActive(i);
  };

  const withSettings = (render: (s: Settings) => JSX.Element) =>
    settingsState.data ? (
      render(settingsState.data)
    ) : settingsState.error ? (
      <LoadError error={settingsState.error} retry={settingsState.refresh} />
    ) : (
      <Loading text="Loading settings…" />
    );

  return (
    <div className="wizard">
      <Brand suffix="setup" />

      <div className="stepper" role="group" aria-label="Setup steps">
        {STEPS.map((s, i) => {
          const cls = i === active ? "current" : i < furthest || i < active ? "done" : "";
          return (
            <Fragment key={s.key}>
              {i > 0 && <span className="line" aria-hidden="true" />}
              <button
                type="button"
                className={`step ${cls}`}
                onClick={() => goto(i)}
                disabled={i > furthest}
                aria-current={i === active ? "step" : undefined}
              >
                <span className="n">{i + 1}</span>
                <span className="label">{s.label}</span>
              </button>
            </Fragment>
          );
        })}
      </div>

      {active === 0 && (
        <AuthStep
          user={status?.user ?? null}
          onConnected={() => {
            refresh();
            setActive(1);
          }}
        />
      )}
      {active === 1 &&
        withSettings((s) => (
          <PlatformsStep
            settings={s}
            onSaved={() => {
              settingsState.refresh();
              refresh();
              setActive(2);
            }}
            onBack={() => setActive(0)}
          />
        ))}
      {active === 2 &&
        withSettings((s) => (
          <ContentStep
            settings={s}
            languages={languages}
            languagesError={languagesState.error}
            onSaved={() => {
              settingsState.refresh();
              refresh();
              setActive(3);
            }}
            onBack={() => setActive(1)}
          />
        ))}
      {active === 3 &&
        withSettings((s) => (
          <FinishStep
            settings={s}
            languages={languages}
            user={status?.user ?? null}
            onBack={() => setActive(2)}
            onDone={() => {
              refresh();
              navigate("/", { replace: true });
            }}
          />
        ))}
    </div>
  );
}

function LoadError({ error, retry }: { error: unknown; retry: () => void }) {
  return (
    <div className="stack">
      <div className="strip danger">Could not load settings: {errorMessage(error)}</div>
      <div>
        <button className="btn" onClick={retry}>
          Retry
        </button>
      </div>
    </div>
  );
}

function AuthStep({ user, onConnected }: { user: User | null; onConnected: (u: User) => void }) {
  return (
    <div className="wizard-step">
      <h1 className="page-title">Connect your GOG account</h1>
      {user && <StatusDot tone="ok">Connected as {user.username}</StatusDot>}
      <AuthorizeForm onConnected={onConnected} currentUser={user} />
    </div>
  );
}

function PlatformsStep({
  settings,
  onSaved,
  onBack,
}: {
  settings: Settings;
  onSaved: () => void;
  onBack: () => void;
}) {
  const [platforms, setPlatforms] = useState<Platform[]>(settings.platforms);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await putSettings({ ...settings, platforms }, null);
      onSaved();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="wizard-step">
      <h1 className="page-title">Which platforms do you want installers for?</h1>
      <p className="lead">
        For every game in your library the offline installers for these platforms will be downloaded, when GOG offers
        them. Pick at least one.
      </p>
      <PlatformPicker value={platforms} onChange={setPlatforms} disabled={busy} />
      {error && <div className="strip danger">{error}</div>}
      <div className="wizard-actions">
        <button className="btn text" onClick={onBack} disabled={busy}>
          Back
        </button>
        <button className="btn primary" onClick={save} disabled={busy || platforms.length === 0}>
          {busy && <Spinner />} Continue
        </button>
      </div>
    </div>
  );
}

function YesNo({
  label,
  hint,
  value,
  onChange,
  name,
  disabled,
}: {
  label: string;
  hint: string;
  value: boolean | null;
  onChange: (v: boolean) => void;
  name: string;
  disabled?: boolean;
}) {
  return (
    <div className="field">
      <span className="field-label" style={{ color: "var(--text)" }}>
        {label}
      </span>
      <span className="hint">{hint}</span>
      <div className="check-inline" role="radiogroup" aria-label={label} style={{ marginTop: 4 }}>
        {[true, false].map((v) => (
          <label key={String(v)} className="check">
            <input type="radio" name={name} checked={value === v} disabled={disabled} onChange={() => onChange(v)} />
            <span>{v ? "Yes" : "No"}</span>
          </label>
        ))}
      </div>
    </div>
  );
}

function ContentStep({
  settings,
  languages,
  languagesError,
  onSaved,
  onBack,
}: {
  settings: Settings;
  languages: LanguageOption[];
  languagesError: unknown;
  onSaved: () => void;
  onBack: () => void;
}) {
  const chosen = settings.content_chosen;
  const [dlc, setDlc] = useState<boolean | null>(chosen ? settings.include_dlc : null);
  const [extras, setExtras] = useState<boolean | null>(chosen ? settings.include_extras : null);
  const [langs, setLangs] = useState<string[]>(settings.languages.length ? settings.languages : ["en"]);
  const [fallback, setFallback] = useState(settings.language_fallback);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const valid = dlc !== null && extras !== null && langs.length > 0;

  const save = async () => {
    if (!valid) return;
    setBusy(true);
    setError(null);
    try {
      await putSettings(
        {
          ...settings,
          include_dlc: dlc,
          include_extras: extras,
          languages: langs,
          language_fallback: fallback,
          content_chosen: true,
        },
        null,
      );
      onSaved();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="wizard-step">
      <h1 className="page-title">What should be downloaded besides the base game?</h1>
      <YesNo
        name="dlc"
        label="Include DLC"
        hint="Installers for downloadable content you own."
        value={dlc}
        onChange={setDlc}
        disabled={busy}
      />
      <YesNo
        name="extras"
        label="Include extras"
        hint="Soundtracks, manuals, wallpapers, artbooks and other bonus content."
        value={extras}
        onChange={setExtras}
        disabled={busy}
      />
      <div className="field">
        <span className="field-label" style={{ color: "var(--text)" }}>
          Languages
        </span>
        <span className="hint">Installers exist per language; pick every language you want. At least one is required.</span>
        {languagesError ? (
          <span className="err-text small">Could not load the language list: {errorMessage(languagesError)}</span>
        ) : languages.length === 0 ? (
          <Loading text="Loading languages…" />
        ) : null}
        <div style={{ marginTop: 4 }}>
          <LanguagePicker options={languages} value={langs} onChange={setLangs} disabled={busy} />
        </div>
      </div>
      <LanguageFallbackSwitch value={fallback} onChange={setFallback} disabled={busy} />
      {error && <div className="strip danger">{error}</div>}
      <div className="wizard-actions">
        <button className="btn text" onClick={onBack} disabled={busy}>
          Back
        </button>
        <button className="btn primary" onClick={save} disabled={busy || !valid}>
          {busy && <Spinner />} Continue
        </button>
      </div>
    </div>
  );
}

function FinishStep({
  settings,
  languages,
  user,
  onBack,
  onDone,
}: {
  settings: Settings;
  languages: LanguageOption[];
  user: User | null;
  onBack: () => void;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => setError(null), [settings]);

  const langNames = useMemo(
    () => settings.languages.map((c) => languages.find((l) => l.code === c)?.name ?? c).join(", "),
    [settings.languages, languages],
  );

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      await completeSetup();
      onDone();
    } catch (e) {
      setError(errorMessage(e));
      setBusy(false);
    }
  };

  return (
    <div className="wizard-step">
      <h1 className="page-title">Ready to go</h1>
      <p className="lead">
        Here is what gogl-watcher will keep in sync. You can change any of this later under Settings.
      </p>
      <dl className="kv summary-list">
        <dt>GOG account</dt>
        <dd>{user ? user.username : <span className="err-text">not connected</span>}</dd>
        <dt>Platforms</dt>
        <dd>
          {settings.platforms.length ? (
            ALL_PLATFORMS.filter((p) => settings.platforms.includes(p))
              .map((p) => PLATFORM_LABELS[p])
              .join(", ")
          ) : (
            <span className="err-text">none chosen</span>
          )}
        </dd>
        <dt>Languages</dt>
        <dd>
          {langNames || <span className="err-text">none chosen</span>}
          {settings.language_fallback && <span className="muted">&nbsp;(with fallback)</span>}
        </dd>
        <dt>DLC</dt>
        <dd>{settings.include_dlc ? "Included" : "Not included"}</dd>
        <dt>Extras</dt>
        <dd>{settings.include_extras ? "Included" : "Not included"}</dd>
        <dt>Check interval</dt>
        <dd>every {settings.check_interval_hours} h</dd>
      </dl>
      <p className="muted small">
        Starting will run the first library sync right away and begin downloading installers into the library folder.
      </p>
      {error && <div className="strip danger">{error}</div>}
      <div className="wizard-actions">
        <button className="btn text" onClick={onBack} disabled={busy}>
          Back
        </button>
        <button className="btn primary" onClick={start} disabled={busy}>
          {busy && <Spinner />} Start
        </button>
      </div>
    </div>
  );
}
