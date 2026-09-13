import { useEffect } from "react";
import { useBlocker } from "react-router-dom";
import { Modal } from "./Modal";

/**
 * Holds up leaving the page while `when` is true: in-app navigation gets a
 * dialog offering to stay or discard the edits, and closing or reloading the tab
 * gets the browser's own prompt.
 */
export function UnsavedChangesGuard({ when }: { when: boolean }) {
  const blocker = useBlocker(when);

  useEffect(() => {
    if (!when) return;
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      // Older browsers need a returnValue to show their prompt.
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [when]);

  // The edits may have been saved or reset while the dialog was open.
  useEffect(() => {
    if (blocker.state === "blocked" && !when) blocker.reset();
  }, [blocker, when]);

  if (blocker.state !== "blocked") return null;
  return (
    <Modal
      title="Unsaved changes"
      onClose={blocker.reset}
      footer={
        <>
          <button className="btn" onClick={blocker.reset} autoFocus>
            Stay on this page
          </button>
          <button className="btn danger" onClick={blocker.proceed}>
            Discard changes
          </button>
        </>
      }
    >
      <p>Your settings have changes that are not saved. Leaving the page discards them.</p>
    </Modal>
  );
}
