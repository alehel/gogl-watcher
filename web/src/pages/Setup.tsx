import { Fragment, useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import {
  completeSetup,
  errorMessage,
  getLanguages,
  getSettings,
  putSettings,
  type DownloadMode,
  type LanguageOption,
  type Platform,
  type Settings,
  type SetupStep,
  type User,
  selectsGames,
} from "../api";
import { AuthorizeForm } from "../components/AuthorizeForm";
import { Loading, Spinner } from "../components/Common";
import { Brand } from "../components/Layout";
import {
  ALL_PLATFORMS,
  DOWNLOAD_MODE_LABELS,
  DownloadModePicker,
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
  { key: "games", label: "Games" },
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

  const withSettings = (render: (s: Settings) => ReactNode) =>
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
          <GamesStep
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
          <PlatformsStep
            settings={s}
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
          <ContentStep
            settings={s}
            languages={languages}
            languagesError={languagesState.error}
            onSaved={() => {
              settingsState.refresh();
              refresh();
              setActive(4);
            }}
            onBack={() => setActive(2)}
          />
        ))}
      {active === 4 &&
        withSettings((s) => (
          <FinishStep
            settings={s}
            languages={languages}
            user={status?.user ?? null}
            onBack={() => setActive(3)}
            onDone={() => {
              refresh();
              navigate("/", { replace: true });
            }}
          />
        ))}
    </div>
  );
}

/**
 * The parts every step after the first has in common: a title, the failure of
 * its save, and the Back/Continue row.
 */
function StepFrame({
  title,
  lead,
  children,
  save,
  onBack,
  nextLabel = "Continue",
  nextDisabled,
}: {
  title: string;
  lead?: ReactNode;
  children: ReactNode;
  save: StepSave;
  onBack: () => void;
  nextLabel?: string;
  nextDisabled?: boolean;
}) {
  return (
    <div className="wizard-step">
      <h1 className="page-title">{title}</h1>
      {lead && <p className="lead">{lead}</p>}
      {children}
      {save.error && <div className="strip danger">{save.error}</div>}
      <div className="wizard-actions">
        <button className="btn text" onClick={onBack} disabled={save.busy}>
          Back
        </button>
        <button className="btn primary" onClick={() => void save.run()} disabled={save.busy || nextDisabled}>
          {save.busy && <Spinner />} {nextLabel}
        </button>
      </div>
    </div>
  );
}

interface StepSave {
  run: () => Promise<void>;
  busy: boolean;
  error: string | null;
}

/**
 * Stores a step's answer and moves on, keeping the message where the step can
 * show it: a wizard that cannot save says so in place instead of toasting.
 */
function useStepSave(store: () => Promise<unknown>, onSaved: () => void): StepSave {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      await store();
      onSaved();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return { run, busy, error };
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

function GamesStep({ settings, onSaved, onBack }: { settings: Settings; onSaved: () => void; onBack: () => void }) {
  const [mode, setMode] = useState<DownloadMode>(settings.download_mode);
  const save = useStepSave(() => putSettings({ ...settings, download_mode: mode }, null), onSaved);

  return (
    <StepFrame
      title="Which games should be downloaded?"
      lead="gogl-watcher can keep an offline copy of your whole GOG library, or only of the games you pick. You can switch later under Settings."
      save={save}
      onBack={onBack}
      nextDisabled={mode === ""}
    >
      <DownloadModePicker value={mode} onChange={setMode} disabled={save.busy} />
      {selectsGames(mode) && (
        <p className="muted small">
          After setup, open the library and tick the games you want. No game is selected to begin with.
        </p>
      )}
    </StepFrame>
  );
}

function PlatformsStep({ settings, onSaved, onBack }: { settings: Settings; onSaved: () => void; onBack: () => void }) {
  const [platforms, setPlatforms] = useState<Platform[]>(settings.platforms);
  const save = useStepSave(() => putSettings({ ...settings, platforms }, null), onSaved);

  return (
    <StepFrame
      title="Which platforms do you want installers for?"
      lead="For every game that is downloaded, the offline installers for these platforms will be fetched when GOG offers them. Pick at least one."
      save={save}
      onBack={onBack}
      nextDisabled={platforms.length === 0}
    >
      <PlatformPicker value={platforms} onChange={setPlatforms} disabled={save.busy} />
    </StepFrame>
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
      <div className="check-inline field-body" role="radiogroup" aria-label={label}>
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
  // The base game is what a backup is for, so it starts as yes; the rest are asked.
  const [installers, setInstallers] = useState<boolean>(chosen ? settings.include_installers : true);
  const [dlc, setDlc] = useState<boolean | null>(chosen ? settings.include_dlc : null);
  const [extras, setExtras] = useState<boolean | null>(chosen ? settings.include_extras : null);
  const [saves, setSaves] = useState<boolean | null>(chosen ? settings.include_saves : null);
  // DESIGN MOCK: artwork is not a setting the backend knows yet; the answer is not saved.
  const [artwork, setArtwork] = useState<boolean | null>(chosen ? false : null);
  const [langs, setLangs] = useState<string[]>(settings.languages.length ? settings.languages : ["en"]);
  const [fallback, setFallback] = useState(settings.language_fallback);

  const valid = dlc !== null && extras !== null && saves !== null && artwork !== null && langs.length > 0;
  const save = useStepSave(
    () =>
      putSettings(
        {
          ...settings,
          include_installers: installers,
          include_dlc: dlc ?? false,
          include_extras: extras ?? false,
          include_saves: saves ?? false,
          languages: langs,
          language_fallback: fallback,
          content_chosen: true,
        },
        null,
      ),
    onSaved,
  );

  return (
    <StepFrame title="What should be downloaded?" save={save} onBack={onBack} nextDisabled={!valid}>
      <YesNo
        name="installers"
        label="Include base game installers"
        hint="The offline installers of the games themselves. Say no to keep only DLC, extras or cloud saves."
        value={installers}
        onChange={setInstallers}
        disabled={save.busy}
      />
      <YesNo
        name="dlc"
        label="Include DLC"
        hint="Installers for downloadable content you own."
        value={dlc}
        onChange={setDlc}
        disabled={save.busy}
      />
      <YesNo
        name="extras"
        label="Include extras"
        hint="Soundtracks, manuals, wallpapers, artbooks and other bonus content."
        value={extras}
        onChange={setExtras}
        disabled={save.busy}
      />
      <YesNo
        name="saves"
        label="Include cloud saves"
        hint="A copy of the save games GOG Galaxy keeps in the cloud, for the games that have any."
        value={saves}
        onChange={setSaves}
        disabled={save.busy}
      />
      <YesNo
        name="artwork"
        label="Include artwork"
        hint="The cover, background, logo, icon and screenshots GOG shows for each game, at full size. For printing your own covers, labels and inlays."
        value={artwork}
        onChange={setArtwork}
        disabled={save.busy}
      />
      <div className="field">
        <span className="field-label">Languages</span>
        <span className="hint">
          Installers exist per language; pick every language you want. At least one is required.
        </span>
        {languagesError ? (
          <span className="err-text small">Could not load the language list: {errorMessage(languagesError)}</span>
        ) : languages.length === 0 ? (
          <Loading text="Loading languages…" />
        ) : null}
        <div className="field-body">
          <LanguagePicker options={languages} value={langs} onChange={setLangs} disabled={save.busy} />
        </div>
      </div>
      <LanguageFallbackSwitch value={fallback} onChange={setFallback} disabled={save.busy} />
    </StepFrame>
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
  const start = useStepSave(completeSetup, onDone);

  const langNames = useMemo(
    () => settings.languages.map((c) => languages.find((l) => l.code === c)?.name ?? c).join(", "),
    [settings.languages, languages],
  );

  return (
    <StepFrame
      title="Ready to go"
      lead="Here is what gogl-watcher will keep in sync. You can change any of this later under Settings."
      save={start}
      onBack={onBack}
      nextLabel="Start"
    >
      <dl className="kv summary-list">
        <dt>GOG account</dt>
        <dd>{user ? user.username : <span className="err-text">not connected</span>}</dd>
        <dt>Games</dt>
        <dd>
          {settings.download_mode ? (
            DOWNLOAD_MODE_LABELS[settings.download_mode]
          ) : (
            <span className="err-text">not chosen</span>
          )}
        </dd>
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
        <dt>Base game installers</dt>
        <dd>{settings.include_installers ? "Included" : "Not included"}</dd>
        <dt>DLC</dt>
        <dd>{settings.include_dlc ? "Included" : "Not included"}</dd>
        <dt>Extras</dt>
        <dd>{settings.include_extras ? "Included" : "Not included"}</dd>
        <dt>Cloud saves</dt>
        <dd>{settings.include_saves ? "Included" : "Not included"}</dd>
        <dt>Check interval</dt>
        <dd>every {settings.check_interval_hours} h</dd>
      </dl>
      <p className="muted small">
        {selectsGames(settings.download_mode)
          ? "Starting will fetch your game list right away. Nothing is downloaded until you select games in the library."
          : "Starting will run the first library sync right away and begin downloading into the library folder."}
      </p>
    </StepFrame>
  );
}
