import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { errorMessage } from "../api";
import { useAction } from "../hooks";
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

interface ToastActionOptions<R> {
  /** Shown when the action succeeded; as a function it can use the result. */
  success?: string | ((result: R) => string);
  /** Prefix for the failure toast, which always ends with the error message. */
  failure?: string;
  /** Called after a successful action, to refresh what it changed. */
  onDone?: () => void;
}

/**
 * An action wired to the toasts: it reports success and failure and refreshes
 * afterwards, so a button only has to say what it does.
 */
export function useToastAction<A extends unknown[], R>(
  action: (...args: A) => Promise<R>,
  options: ToastActionOptions<R> = {},
) {
  const toast = useToast();
  const { run, pending, error } = useAction(action);
  // Kept in a ref so the returned callback is stable across renders.
  const optionsRef = useRef(options);
  optionsRef.current = options;

  const perform = useCallback(
    async (...args: A) => {
      const { success, failure, onDone } = optionsRef.current;
      try {
        const result = await run(...args);
        const text = typeof success === "function" ? success(result) : success;
        if (text) toast.success(text);
        onDone?.();
      } catch (e) {
        toast.error(failure ? `${failure}: ${errorMessage(e)}` : errorMessage(e));
      }
    },
    [run, toast],
  );
  return { run: perform, pending, error };
}
