import { useEffect, useMemo, useState } from "react";
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
import { IconAlert, IconCheck, IconLogo } from "../components/Icons";
import {
  ALL_PLATFORMS,
  LanguageFallbackSwitch,
  LanguagePicker,
  PlatformPicker,
} from "../components/SettingsFields";
import { useStatus } from "../components/StatusContext";
import { useAsync } from "../hooks";
import { PLATFORM_LABELS } from "../format";

const STEPS = [
  { key: "auth", label: "1. Authorize" },
  { key: "platforms", label: "2. Platforms" },
  { key: "content", label: "3. Content" },
  { key: "done", label: "4. Finish" },
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

  return (
    <div className="wizard">
      <div className="brand">
        <span className="brand-mark">
          <IconLogo />
        </span>
        gogl-watcher setup
      </div>

      <div className="stepper" role="group" aria-label="Setup steps">
        {STEPS.map((s, i) => {
          const cls = i === active ? "current" : i < furthest || i < active ? "done" : "";
          return (
            <button
              key={s.key}
              type="button"
              className={`step ${cls}`}
              onClick={() => goto(i)}
              disabled={i > furthest}
              aria-current={i === active ? "step" : undefined}
            >
              <span className="bar" />
              <span className="label">{s.label}</span>
            </button>
          );
        })}
      </div>

      <div className="card">
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
          (settingsState.data ? (
            <PlatformsStep
              settings={settingsState.data}
              onSaved={() => {
                settingsState.refresh();
                refresh();
                setActive(2);
              }}
              onBack={() => setActive(0)}
            />
          ) : settingsState.error ? (
            <LoadError error={settingsState.error} retry={settingsState.refresh} />
          ) : (
            <Loading text="Loading settings…" />
          ))}
        {active === 2 &&
          (settingsState.data ? (
            <ContentStep
              settings={settingsState.data}
              languages={languages}
              languagesError={languagesState.error}
              onSaved={() => {
                settingsState.refresh();
                refresh();
                setActive(3);
              }}
              onBack={() => setActive(1)}
            />
          ) : settingsState.error ? (
            <LoadError error={settingsState.error} retry={settingsState.refresh} />
          ) : (
            <Loading text="Loading settings…" />
          ))}
        {active === 3 &&
          (settingsState.data ? (
            <FinishStep
              settings={settingsState.data}
              languages={languages}
              user={status?.user ?? null}
              onBack={() => setActive(2)}
              onDone={() => {
                refresh();
                navigate("/", { replace: true });
              }}
            />
          ) : settingsState.error ? (
            <LoadError error={settingsState.error} retry={settingsState.refresh} />
          ) : (
            <Loading text="Loading settings…" />
          ))}
      </div>
    </div>
  );
}

function LoadError({ error, retry }: { error: unknown; retry: () => void }) {
  return (
    <div className="stack">
      <div className="notice error">
        <IconAlert />
        <span>Could not load settings: {errorMessage(error)}</span>
      </div>
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
    <>
      <h1>Connect your GOG account</h1>
      {user && (
        <div className="auth-user">
          <IconCheck />
          Connected as {user.username}
        </div>
      )}
      <AuthorizeForm onConnected={onConnected} currentUser={user} />
    </>
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
    <>
      <h1>Which platforms do you want installers for?</h1>
      <p className="lead">
        For every game in your library the offline installers for these platforms will be downloaded, when GOG offers
        them. Pick at least one.
      </p>
      <PlatformPicker value={platforms} onChange={setPlatforms} disabled={busy} />
      {error && (
        <div className="notice error">
          <IconAlert />
          <span>{error}</span>
        </div>
      )}
      <div className="actions">
        <button className="btn" onClick={onBack} disabled={busy}>
          Back
        </button>
        <button className="btn primary" onClick={save} disabled={busy || platforms.length === 0}>
          {busy && <Spinner />} Continue
        </button>
      </div>
    </>
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
      <span className="field-label">{label}</span>
      <span className="hint">{hint}</span>
      <div className="check-row" role="radiogroup" aria-label={label}>
        {[true, false].map((v) => (
          <label key={String(v)} className={`check ${value === v ? "checked" : ""}`}>
            <input
              type="radio"
              name={name}
              checked={value === v}
              disabled={disabled}
              onChange={() => onChange(v)}
            />
            <span className="check-text">
              <span>{v ? "Yes" : "No"}</span>
            </span>
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
    <>
      <h1>What should be downloaded besides the base game?</h1>
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
        <span className="field-label">Languages</span>
        <span className="hint">Installers exist per language; pick every language you want. At least one is required.</span>
        {languagesError ? (
          <span className="err-text small">Could not load the language list: {errorMessage(languagesError)}</span>
        ) : languages.length === 0 ? (
          <Loading text="Loading languages…" />
        ) : null}
        <LanguagePicker options={languages} value={langs} onChange={setLangs} disabled={busy} />
      </div>
      <LanguageFallbackSwitch value={fallback} onChange={setFallback} disabled={busy} />
      {error && (
        <div className="notice error">
          <IconAlert />
          <span>{error}</span>
        </div>
      )}
      <div className="actions">
        <button className="btn" onClick={onBack} disabled={busy}>
          Back
        </button>
        <button className="btn primary" onClick={save} disabled={busy || !valid}>
          {busy && <Spinner />} Continue
        </button>
      </div>
    </>
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
    <>
      <h1>Ready to go</h1>
      <p className="lead">
        Here is what gogl-watcher will keep in sync. You can change any of this later under Settings.
      </p>
      <dl className="summary">
        <dt>GOG account</dt>
        <dd>{user ? user.username : <span className="err-text">not connected</span>}</dd>
        <dt>Platforms</dt>
        <dd>
          {settings.platforms.length
            ? ALL_PLATFORMS.filter((p) => settings.platforms.includes(p)).map((p) => PLATFORM_LABELS[p]).join(", ")
            : <span className="err-text">none chosen</span>}
        </dd>
        <dt>Languages</dt>
        <dd>
          {langNames || <span className="err-text">none chosen</span>}
          {settings.language_fallback && <span className="muted"> (with fallback)</span>}
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
      {error && (
        <div className="notice error">
          <IconAlert />
          <span>{error}</span>
        </div>
      )}
      <div className="actions">
        <button className="btn" onClick={onBack} disabled={busy}>
          Back
        </button>
        <button className="btn primary" onClick={start} disabled={busy}>
          {busy ? <Spinner /> : <IconCheck />} Start
        </button>
      </div>
    </>
  );
}
