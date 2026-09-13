import { useCallback, useState } from "react";
import {
  errorMessage,
  getLanguages,
  previewFromError,
  setGameOptions,
  type OnRemoved,
  type SettingsPreview,
} from "../api";
import { useAsync } from "../hooks";
import { RemovalConfirmModal } from "./SettingsFields";
import { useToast } from "./Toast";

export interface GameOptionsValue {
  include_dlc: boolean;
  include_extras: boolean;
}

interface Pending {
  options: GameOptionsValue;
  preview: SettingsPreview;
}

/**
 * Opts one game in to or out of DLC and extras on its own. Opting out of downloaded
 * files makes the backend ask whether to keep or delete them; the hook shows that
 * dialog and re-submits with the answer. Render `modal` somewhere in the page.
 */
export function useGameOptions(id: number | string, onChanged: () => void) {
  const toast = useToast();
  const languages = useAsync(getLanguages);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);

  const submit = useCallback(
    async (options: GameOptionsValue, onRemoved: OnRemoved) => {
      setBusy(true);
      try {
        await setGameOptions(id, options, onRemoved);
        setPending(null);
        toast.success("Game options saved");
        onChanged();
      } catch (e) {
        const preview = previewFromError(e);
        if (preview) {
          setPending({ options, preview });
        } else {
          setPending(null);
          toast.error(`Could not change the game's options: ${errorMessage(e)}`);
          onChanged();
        }
      } finally {
        setBusy(false);
      }
    },
    [id, onChanged, toast],
  );

  const setOptions = useCallback((options: GameOptionsValue) => submit(options, null), [submit]);

  const modal = pending ? (
    <RemovalConfirmModal
      removed={pending.preview.removed}
      reasons={pending.preview.reasons}
      languages={languages.data?.languages ?? []}
      busy={busy}
      onCancel={() => {
        setPending(null);
        onChanged();
      }}
      onKeep={() => void submit(pending.options, "keep")}
      onDelete={() => void submit(pending.options, "delete")}
    />
  ) : null;

  return { setOptions, busy, modal };
}
