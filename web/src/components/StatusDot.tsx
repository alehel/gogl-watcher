import type { ReactNode } from "react";
import type { FileStatus, GameStatus } from "../api";

export type Tone = "ok" | "info" | "warn" | "danger" | "muted";

export function gameStatusTone(status: GameStatus): Tone {
  switch (status) {
    case "complete":
      return "ok";
    case "downloading":
      return "info";
    case "pending":
    case "partial":
      return "warn";
    case "error":
      return "danger";
    case "unavailable":
    case "unsynced":
    case "unselected":
    default:
      return "muted";
  }
}

export function fileStatusTone(status: FileStatus): Tone {
  switch (status) {
    case "done":
      return "ok";
    case "downloading":
      return "info";
    case "pending":
      return "warn";
    case "error":
      return "danger";
    case "inactive":
    default:
      return "muted";
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
  unselected: "Not selected",
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

export function fileStatusLabel(status: FileStatus): string {
  return FILE_LABELS[status] ?? status;
}

/** 7px coloured dot followed by plain text. */
export function StatusDot({ tone, children, className }: { tone: Tone; children: ReactNode; className?: string }) {
  return (
    <span className={`status${className ? ` ${className}` : ""}`}>
      <span className={`dot ${tone}`} aria-hidden="true" />
      {children}
    </span>
  );
}

export function GameStatusDot({ status }: { status: GameStatus }) {
  return <StatusDot tone={gameStatusTone(status)}>{gameStatusLabel(status)}</StatusDot>;
}

export function FileStatusDot({ status }: { status: FileStatus }) {
  return <StatusDot tone={fileStatusTone(status)}>{fileStatusLabel(status)}</StatusDot>;
}
