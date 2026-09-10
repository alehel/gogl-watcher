import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { IconClose } from "./Icons";

type Kind = "info" | "success" | "error";
interface Toast {
  id: number;
  kind: Kind;
  text: string;
}
interface ToastApi {
  push: (text: string, kind?: Kind) => void;
  success: (text: string) => void;
  error: (text: string) => void;
}

const TONE: Record<Kind, string> = { info: "info", success: "ok", error: "danger" };

const ToastContext = createContext<ToastApi | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => {
    setToasts((t) => t.filter((x) => x.id !== id));
  }, []);

  const push = useCallback(
    (text: string, kind: Kind = "info") => {
      const id = nextId.current++;
      setToasts((t) => [...t.slice(-4), { id, kind, text }]);
      window.setTimeout(() => dismiss(id), kind === "error" ? 8000 : 4000);
    },
    [dismiss],
  );

  const api = useMemo<ToastApi>(
    () => ({
      push,
      success: (text) => push(text, "success"),
      error: (text) => push(text, "error"),
    }),
    [push],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="toasts" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className="toast">
            <span className={`dot ${TONE[t.kind]}`} aria-hidden="true" />
            <span className="text">{t.text}</span>
            <button className="btn icon" onClick={() => dismiss(t.id)} aria-label="Dismiss">
              <IconClose />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useToast must be used inside ToastProvider");
  return ctx;
}
