import { createContext, useContext, type ReactNode } from "react";
import { getStatus, type Status } from "../api";
import { usePolling } from "../hooks";

interface StatusCtx {
  status: Status | null;
  error: unknown;
  loading: boolean;
  refresh: () => void;
}

const Ctx = createContext<StatusCtx | null>(null);

/** Polls GET /api/status every 2 s for the whole app. */
export function StatusProvider({ children }: { children: ReactNode }) {
  const { data, error, loading, refresh } = usePolling(getStatus, 2000);
  return <Ctx.Provider value={{ status: data, error, loading, refresh }}>{children}</Ctx.Provider>;
}

export function useStatus(): StatusCtx {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useStatus must be used inside StatusProvider");
  return ctx;
}
