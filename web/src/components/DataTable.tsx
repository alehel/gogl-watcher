import type { ReactNode } from "react";

/** Plain table inside a horizontally scrolling container (for narrow screens). */
export function DataTable({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className="table-wrap">
      <table className={`table${className ? ` ${className}` : ""}`}>{children}</table>
    </div>
  );
}
