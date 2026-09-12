import { estimateSettings, type SettingsEstimate, type Settings } from "../api";
import { formatBytes } from "../format";
import { useAsync, useDebounced } from "../hooks";

/** The settings that decide which files are wanted; nothing else moves a total. */
function planKey(s: Settings): string {
  return JSON.stringify([
    s.platforms,
    s.languages,
    s.language_fallback,
    s.include_dlc,
    s.include_extras,
  ]);
}

/**
 * Estimates a combination the user is considering together with the one in
 * force, so that the difference between them is honest: the catalog grows while
 * the background scan runs, and two estimates taken minutes apart would differ
 * by whatever was measured in between rather than by the change being made.
 *
 * Only the choices that decide which files are wanted are watched, so editing
 * the speed limit asks nothing, and edits are debounced so ticking a row of
 * checkboxes asks once.
 */
export function useEstimatePair(form: Settings, saved: Settings) {
  const savedKey = planKey(saved);
  const debounced = useDebounced(planKey(form), 400);
  return useAsync(async () => {
    const edited = await estimateSettings(form);
    if (debounced === savedKey) return { edited, saved: edited };
    return { edited, saved: await estimateSettings(saved) };
  }, [debounced, savedKey]);
}

/**
 * One line on what a backup of the whole library costs. It deliberately says
 * "about": GOG's manifest sizes are a hair off the bytes that arrive, and the
 * games that have not been measured yet are missing from the total altogether.
 */
export function EstimateNote({
  estimate,
  lead = "A full backup of your library needs about",
  className = "summary",
}: {
  estimate: SettingsEstimate | null | undefined;
  lead?: string;
  className?: string;
}) {
  if (!estimate) return null;
  const { bytes, files, games_scanned, games_total } = estimate;
  const left = games_total - games_scanned;

  if (games_scanned === 0) {
    return (
      <p className={`${className} faint`}>
        {games_total > 0
          ? `Still measuring what your library would need — none of its ${games_total.toLocaleString()} games have been sized yet.`
          : "Sizes are measured once the library has been synced."}
      </p>
    );
  }
  return (
    <p className={className}>
      {lead} <strong>{formatBytes(bytes)}</strong> in {files.toLocaleString()}{" "}
      {files === 1 ? "file" : "files"}
      {left > 0 ? (
        <>
          {" "}
          — measured from {games_scanned.toLocaleString()} of {games_total.toLocaleString()} games so far, so
          the rest is still to come.
        </>
      ) : (
        "."
      )}
    </p>
  );
}
