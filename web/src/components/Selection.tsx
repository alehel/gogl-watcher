import { useCallback, useState } from "react";
import { errorMessage, getLanguages, previewFromError, setGameSelection, type OnRemoved, type SettingsPreview } from "../api";
import { plural } from "../format";
import { useAsync } from "../hooks";
import { RemovalConfirmModal } from "./SettingsFields";
import { useToast } from "./Toast";

interface Pending {
  ids: number[];
  preview: SettingsPreview;
}

/**
 * Selects or deselects games for download. Deselecting games with downloaded files
 * makes the backend ask whether to keep or delete them; the hook shows that dialog
 * and re-submits with the answer. Render `modal` somewhere in the page.
 */
export function useGameSelection(onChanged: () => void) {
  const toast = useToast();
  const languages = useAsync(getLanguages);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);

  const submit = useCallback(
    async (ids: number[], selected: boolean, onRemoved: OnRemoved) => {
      if (ids.length === 0) return;
      setBusy(true);
      try {
        await setGameSelection(ids, selected, onRemoved);
        setPending(null);
        const what = plural(ids.length, "game");
        toast.success(selected ? `${what} selected — fetching files` : `${what} removed from the selection`);
        onChanged();
      } catch (e) {
        const preview = previewFromError(e);
        if (preview) {
          setPending({ ids, preview });
        } else {
          setPending(null);
          toast.error(`Could not change the selection: ${errorMessage(e)}`);
          // The optimistic checkbox has flipped; a refresh snaps it back to the server state.
          onChanged();
        }
      } finally {
        setBusy(false);
      }
    },
    [onChanged, toast],
  );

  const setSelection = useCallback((ids: number[], selected: boolean) => submit(ids, selected, null), [submit]);

  const modal = pending ? (
    <RemovalConfirmModal
      removed={pending.preview.removed}
      reasons={pending.preview.reasons}
      languages={languages.data?.languages ?? []}
      busy={busy}
      onCancel={() => {
        setPending(null);
        onChanged(); // nothing changed on the server; undo the optimistic flip
      }}
      onKeep={() => void submit(pending.ids, false, "keep")}
      onDelete={() => void submit(pending.ids, false, "delete")}
    />
  ) : null;

  return { setSelection, busy, modal };
}
