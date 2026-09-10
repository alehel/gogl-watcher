import type { ReactNode } from "react";
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { errorMessage, NetworkError } from "./api";
import { Loading } from "./components/Common";
import { Layout } from "./components/Layout";
import { StatusProvider, useStatus } from "./components/StatusContext";
import { ToastProvider } from "./components/Toast";
import { AuthPage } from "./pages/Auth";
import { DashboardPage } from "./pages/Dashboard";
import { DownloadsPage } from "./pages/Downloads";
import { GamePage } from "./pages/Game";
import { LibraryPage } from "./pages/Library";
import { LogsPage } from "./pages/Logs";
import { SettingsPage } from "./pages/Settings";
import { SetupPage } from "./pages/Setup";

/** Blocks rendering until the first /api/status response; redirects between wizard and app. */
function Gate({ children, wizard }: { children: ReactNode; wizard: boolean }) {
  const { status, error, loading, refresh } = useStatus();
  const location = useLocation();

  if (!status) {
    if (loading) {
      return (
        <div className="fullscreen-center">
          <Loading text="Connecting to gogl-watcher…" />
        </div>
      );
    }
    const network = error instanceof NetworkError;
    return (
      <div className="fullscreen-center">
        <div className="box stack">
          <h1>{network ? "API unreachable" : "Something went wrong"}</h1>
          <p className="muted">
            {network
              ? "The gogl-watcher backend is not responding. It may still be starting up; this page keeps retrying."
              : errorMessage(error)}
          </p>
          <div>
            <button className="btn" onClick={refresh}>
              Retry now
            </button>
          </div>
        </div>
      </div>
    );
  }

  if (!status.setup_complete && !wizard) return <Navigate to="/setup" replace state={{ from: location }} />;
  if (status.setup_complete && wizard) return <Navigate to="/" replace />;
  return <>{children}</>;
}

export default function App() {
  return (
    <ToastProvider>
      <StatusProvider>
        <BrowserRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
          <Routes>
            <Route
              path="/setup"
              element={
                <Gate wizard>
                  <SetupPage />
                </Gate>
              }
            />
            <Route
              element={
                <Gate wizard={false}>
                  <Layout />
                </Gate>
              }
            >
              <Route index element={<DashboardPage />} />
              <Route path="/library" element={<LibraryPage />} />
              <Route path="/library/:id" element={<GamePage />} />
              <Route path="/downloads" element={<DownloadsPage />} />
              <Route path="/logs" element={<LogsPage />} />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="/auth" element={<AuthPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </StatusProvider>
    </ToastProvider>
  );
}
