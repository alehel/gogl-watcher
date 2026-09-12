import { pauseDownloads, resumeDownloads } from "../api";
import { useToastAction } from "./Toast";

/** Pauses or resumes the download queue; shown on the dashboard and the downloads page. */
export function PauseButton({ paused, onChanged }: { paused: boolean; onChanged: () => void }) {
  const toggle = useToastAction(paused ? resumeDownloads : pauseDownloads, {
    success: (res) => (res.paused ? "Downloads paused" : "Downloads resumed"),
    onDone: onChanged,
  });
  return (
    <button className="btn" onClick={() => void toggle.run()} disabled={toggle.pending}>
      {paused ? "Resume" : "Pause"}
    </button>
  );
}
