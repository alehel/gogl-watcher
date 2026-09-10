import { useEffect, useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { NetworkError } from "../api";
import { useStatus } from "./StatusContext";
import {
  IconAlert,
  IconClose,
  IconDashboard,
  IconDownload,
  IconLibrary,
  IconLogo,
  IconLogs,
  IconMenu,
  IconSettings,
} from "./Icons";

const NAV = [
  { to: "/", label: "Dashboard", icon: IconDashboard, end: true },
  { to: "/library", label: "Library", icon: IconLibrary },
  { to: "/downloads", label: "Downloads", icon: IconDownload },
  { to: "/logs", label: "Logs", icon: IconLogs },
  { to: "/settings", label: "Settings", icon: IconSettings },
];

function NavLinks({ onNavigate, activeDownloads }: { onNavigate?: () => void; activeDownloads: number }) {
  return (
    <nav className="nav" aria-label="Main">
      {NAV.map(({ to, label, icon: Icon, end }) => (
        <NavLink key={to} to={to} end={end} onClick={onNavigate}>
          <Icon />
          <span>{label}</span>
          {to === "/downloads" && activeDownloads > 0 && <span className="badge blue plain">{activeDownloads}</span>}
        </NavLink>
      ))}
    </nav>
  );
}

function Brand() {
  return (
    <Link to="/" className="brand">
      <span className="brand-mark">
        <IconLogo />
      </span>
      gogl-watcher
    </Link>
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

  return (
    <div className="app">
      <aside className="sidebar">
        <Brand />
        <NavLinks activeDownloads={activeDownloads} />
        <div className="sidebar-foot">
          {status?.user && (
            <span className="user" title={`GOG user ${status.user.username}`}>
              {status.user.username}
            </span>
          )}
          <span>{status ? `v${status.version}` : "—"}</span>
        </div>
      </aside>

      <div className="main">
        <header className="topbar">
          <Brand />
          <span className="spacer" />
          <button className="btn ghost icon-only" onClick={() => setOpen(true)} aria-label="Open menu">
            <IconMenu />
          </button>
        </header>

        {open && (
          <div className="drawer" onMouseDown={(e) => e.target === e.currentTarget && setOpen(false)}>
            <div className="panel">
              <div className="panel-head">
                <span>Menu</span>
                <button className="btn ghost icon-only" onClick={() => setOpen(false)} aria-label="Close menu">
                  <IconClose />
                </button>
              </div>
              <NavLinks activeDownloads={activeDownloads} onNavigate={() => setOpen(false)} />
              <div className="sidebar-foot">
                {status?.user && <span className="user">{status.user.username}</span>}
                <span>{status ? `v${status.version}` : "—"}</span>
              </div>
            </div>
          </div>
        )}

        {unreachable && (
          <div className="banner warn" role="status">
            <IconAlert />
            <span className="grow">API unreachable — the backend is not responding. Retrying automatically.</span>
          </div>
        )}
        {needsAuth && location.pathname !== "/auth" && (
          <div className="banner danger" role="alert">
            <IconAlert />
            <span className="grow">
              {status?.auth_error
                ? `GOG session lost: ${status.auth_error}.`
                : "Not connected to GOG."}{" "}
              Syncing and downloads are stopped until you <Link to="/auth">re-authorize</Link>.
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
