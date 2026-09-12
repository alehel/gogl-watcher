import { useState, type FormEvent } from "react";
import { errorMessage, getAuthUrl, postAuthCode, type User } from "../api";
import { useAsync } from "../hooks";
import { IconExternal } from "./Icons";
import { Spinner } from "./Common";
import { StatusDot } from "./StatusDot";

interface Props {
  onConnected: (user: User) => void;
  /** Currently connected user (for the re-authorize page). */
  currentUser?: User | null;
}

/** Shared GOG authorization UI used by the setup wizard and /auth. */
export function AuthorizeForm({ onConnected, currentUser }: Props) {
  const urlState = useAsync(getAuthUrl);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [connected, setConnected] = useState<User | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const value = code.trim();
    if (!value) {
      setError("Paste the redirect URL or the code first.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await postAuthCode(value);
      setConnected(res.user);
      setCode("");
      onConnected(res.user);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <p className="lead">
        gogl-watcher signs in the same way GOG Galaxy does. Your credentials never touch this app: you log in on gog.com
        and paste back the one-time code GOG hands out.
      </p>
      {currentUser && !connected && (
        <div className="strip info">
          <span>
            Currently connected as <strong>{currentUser.username}</strong>. Authorizing again replaces the stored token.
          </span>
        </div>
      )}
      <ol className="muted">
        <li>Open the GOG login page (it opens in a new tab) and sign in.</li>
        <li>
          You will land on a blank page whose address starts with <code>https://embed.gog.com/on_login_success</code>.
        </li>
        <li>
          Copy the full URL of that page, or just the <code>code=</code> value, and paste it below.
        </li>
      </ol>

      <div className="btn-row">
        {urlState.data ? (
          <a className="btn" href={urlState.data.url} target="_blank" rel="noopener noreferrer">
            Open GOG login <IconExternal />
          </a>
        ) : urlState.error ? (
          <span className="small err-text">
            Could not get the login URL: {errorMessage(urlState.error)}.{" "}
            <button className="link-btn" onClick={urlState.refresh}>
              Retry
            </button>
          </span>
        ) : (
          <button className="btn" disabled>
            <Spinner /> Preparing login link…
          </button>
        )}
      </div>

      <form onSubmit={submit} className="stack">
        <div className="field">
          <label htmlFor="auth-code">Redirect URL or code</label>
          <input
            id="auth-code"
            className="input mono"
            placeholder="https://embed.gog.com/on_login_success?origin=client&code=…"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            disabled={busy}
          />
        </div>
        {error && (
          <div className="strip danger" role="alert">
            {error}
          </div>
        )}
        {connected && <StatusDot tone="ok">Connected as {connected.username}</StatusDot>}
        <div className="btn-row">
          <button className="btn primary" type="submit" disabled={busy || !code.trim()}>
            {busy && <Spinner />} Connect
          </button>
        </div>
      </form>
    </div>
  );
}
