import { useEffect, useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { NetworkError } from "../api";
import { useStatus } from "./StatusContext";
import { IconClose } from "./Icons";

const NAV = [
  { to: "/", label: "Dashboard", end: true },
  { to: "/library", label: "Library" },
  { to: "/downloads", label: "Downloads" },
  { to: "/logs", label: "Logs" },
  { to: "/settings", label: "Settings" },
];

function NavLinks({ onNavigate, activeDownloads }: { onNavigate?: () => void; activeDownloads: number }) {
  return (
    <nav className="nav" aria-label="Main">
      {NAV.map(({ to, label, end }) => (
        <NavLink key={to} to={to} end={end} onClick={onNavigate}>
          <span>{label}</span>
          {to === "/downloads" && activeDownloads > 0 && (
            <span className="count" aria-label={`${activeDownloads} active`}>
              {activeDownloads}
            </span>
          )}
        </NavLink>
      ))}
    </nav>
  );
}

export function Brand({ suffix }: { suffix?: string }) {
  return (
    <Link to="/" className="brand">
      <span className="brand-dot" aria-hidden="true" />
      gogl-watcher{suffix ? ` ${suffix}` : ""}
    </Link>
  );
}

function Foot({ user, version }: { user: string | undefined; version: string | undefined }) {
  const showVersion = !!version && version !== "dev";
  if (!user && !showVersion) return null;
  return (
    <div className="sidebar-foot">
      {user && (
        <span className="user" title={`GOG user ${user}`}>
          {user}
        </span>
      )}
      {showVersion && <span className="version">v{version}</span>}
    </div>
  );
}

export function Layout() {
  const { status, error } = useStatus();
  const [open, setOpen] = useState(false);
  const location = useLocation();

  useEffect(() => {
    setOpen(false);
  }, [location.pathname]);

  const activeDownloads = status?.downloads.active ?? 0;
  const needsAuth = !!status && status.setup_complete && (!status.authenticated || !!status.auth_error);
  const unreachable = error instanceof NetworkError;
  const foot = <Foot user={status?.user?.username} version={status?.version} />;

  return (
    <div className="app">
      <aside className="sidebar">
        <Brand />
        <NavLinks activeDownloads={activeDownloads} />
        {foot}
      </aside>

      <div className="main">
        <header className="topbar">
          <Brand />
          <span className="spacer" />
          <button className="btn text" onClick={() => setOpen(true)} aria-haspopup="dialog" aria-expanded={open}>
            Menu
          </button>
        </header>

        {open && (
          <div className="drawer" onMouseDown={(e) => e.target === e.currentTarget && setOpen(false)}>
            <div className="panel" role="dialog" aria-label="Menu">
              <div className="panel-head">
                <span>Menu</span>
                <button className="btn icon" onClick={() => setOpen(false)} aria-label="Close menu">
                  <IconClose />
                </button>
              </div>
              <NavLinks activeDownloads={activeDownloads} onNavigate={() => setOpen(false)} />
              {foot}
            </div>
          </div>
        )}

        {unreachable && (
          <div className="strip top warn" role="status">
            API unreachable — the backend is not responding. Retrying automatically.
          </div>
        )}
        {needsAuth && location.pathname !== "/auth" && (
          <div className="strip top danger" role="alert">
            <span>
              {status?.auth_error ? `GOG session lost: ${status.auth_error}.` : "Not connected to GOG."} Syncing and
              downloads are stopped until you <Link to="/auth">re-authorize</Link>.
            </span>
          </div>
        )}

        <main className="content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
