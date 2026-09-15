import type { SVGProps } from "react";

type P = SVGProps<SVGSVGElement>;
const base = (props: P) => ({
  className: `icon${props.className ? ` ${props.className}` : ""}`,
  viewBox: "0 0 24 24",
  "aria-hidden": true,
  ...props,
});

/**
 * The app mark, the same drawing as the favicon. It is filled rather than
 * stroked, so it is kept out of the currentColor icon set above.
 */
export const Logo = (p: P) => (
  <svg className="brand-mark" viewBox="0 0 32 32" aria-hidden="true" {...p}>
    <rect width="32" height="32" rx="7" fill="var(--accent)" />
    <path d="M9 11h14v10H9z" fill="none" stroke="var(--surface)" strokeWidth="2.2" />
    <path d="M13 15h6" stroke="var(--surface)" strokeWidth="2.2" strokeLinecap="round" />
  </svg>
);

export const IconMenu = (p: P) => (
  <svg {...base(p)}>
    <path d="M4 7h16M4 12h16M4 17h16" />
  </svg>
);
export const IconClose = (p: P) => (
  <svg {...base(p)}>
    <path d="M6 6l12 12M18 6L6 18" />
  </svg>
);
export const IconExternal = (p: P) => (
  <svg {...base(p)}>
    <path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5" />
  </svg>
);
export const IconCheck = (p: P) => (
  <svg {...base(p)}>
    <path d="M5 12l5 5L20 7" />
  </svg>
);
export const IconBack = (p: P) => (
  <svg {...base(p)}>
    <path d="M15 5l-7 7 7 7" />
  </svg>
);
