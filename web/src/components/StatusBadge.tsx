import type { FileStatus, GameStatus } from "../api";

export type Tone = "green" | "blue" | "amber" | "red" | "grey" | "accent";

export function gameStatusTone(status: GameStatus): Tone {
  switch (status) {
    case "complete":
      return "green";
    case "downloading":
      return "blue";
    case "pending":
    case "partial":
      return "amber";
    case "error":
      return "red";
    case "unavailable":
    case "unsynced":
    default:
      return "grey";
  }
}

export function fileStatusTone(status: FileStatus): Tone {
  switch (status) {
    case "done":
      return "green";
    case "downloading":
      return "blue";
    case "pending":
      return "amber";
    case "error":
      return "red";
    case "inactive":
    default:
      return "grey";
  }
}

const GAME_LABELS: Record<GameStatus, string> = {
  complete: "Complete",
  downloading: "Downloading",
  pending: "Pending",
  partial: "Partial",
  error: "Error",
  unavailable: "Unavailable",
  unsynced: "Not synced",
};

const FILE_LABELS: Record<FileStatus, string> = {
  done: "Done",
  downloading: "Downloading",
  pending: "Pending",
  error: "Error",
  inactive: "Inactive",
};

export function gameStatusLabel(status: GameStatus): string {
  return GAME_LABELS[status] ?? status;
}

export function GameStatusBadge({ status }: { status: GameStatus }) {
  return <span className={`badge ${gameStatusTone(status)}`}>{gameStatusLabel(status)}</span>;
}

export function FileStatusBadge({ status }: { status: FileStatus }) {
  return <span className={`badge ${fileStatusTone(status)}`}>{FILE_LABELS[status] ?? status}</span>;
}
