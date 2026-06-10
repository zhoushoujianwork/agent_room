/**
 * Stroke-icon set ported from references/agent-room-design/app/src/icons.jsx.
 * Each name maps to a flat <path> blob inside a 24x24 viewBox.
 */

import type { CSSProperties } from "react";

const ICON_PATHS: Record<string, string> = {
  home: '<path d="M3 11.5 12 4l9 7.5"/><path d="M5 10v9.5h14V10"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/>',
  send: '<path d="M7 17 17 7"/><path d="M8 7h9v9"/>',
  bot: '<rect x="5" y="8" width="14" height="11" rx="2.5"/><path d="M12 4v4M9 13h.01M15 13h.01M3 13v3M21 13v3"/>',
  terminal: '<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="M7 9l3 3-3 3M13 15h4"/>',
  activity: '<path d="M3 12h4l2.5 7 5-14L17 12h4"/>',
  settings: '<circle cx="12" cy="12" r="3.2"/><path d="M12 3.5v2M12 18.5v2M4.6 7.5l1.7 1M17.7 15.5l1.7 1M4.6 16.5l1.7-1M17.7 8.5l1.7-1"/>',
  refresh: '<path d="M20 11a8 8 0 0 0-14-4.5L4 8M4 4v4h4"/><path d="M4 13a8 8 0 0 0 14 4.5L20 16M20 20v-4h-4"/>',
  chevronDown: '<path d="M6 9l6 6 6-6"/>',
  chevronRight: '<path d="M9 6l6 6-6 6"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7"/>',
  checkSmall: '<path d="M4 12l5 5L20 6"/>',
  x: '<path d="M6 6l12 12M18 6L6 18"/>',
  download: '<path d="M12 3v12M7 11l5 5 5-5"/><path d="M5 20h14"/>',
  shield: '<path d="M12 3l7 3v5c0 4.4-3 7.7-7 9-4-1.3-7-4.6-7-9V6z"/>',
  shieldCheck: '<path d="M12 3l7 3v5c0 4.4-3 7.7-7 9-4-1.3-7-4.6-7-9V6z"/><path d="M9 12l2 2 4-4"/>',
  github: '<path d="M9 19c-4.3 1.4-4.3-2.5-6-3m12 5v-3.5c0-1 .1-1.4-.5-2 2.8-.3 5.5-1.4 5.5-6a4.6 4.6 0 0 0-1.3-3.2 4.2 4.2 0 0 0-.1-3.2s-1.1-.3-3.5 1.3a12 12 0 0 0-6 0C6.2 2.3 5.1 2.6 5.1 2.6a4.2 4.2 0 0 0-.1 3.2A4.6 4.6 0 0 0 3.7 9c0 4.6 2.7 5.7 5.5 6-.6.6-.6 1.2-.5 2V20"/>',
  arrowUpRight: '<path d="M7 17 17 7M8 7h9v9"/>',
  users: '<circle cx="9" cy="8" r="3.2"/><path d="M3.5 19a5.5 5.5 0 0 1 11 0"/><path d="M16 5.5a3.2 3.2 0 0 1 0 6M20.5 19a5.5 5.5 0 0 0-3-4.9"/>',
  clock: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/>',
  alert: '<path d="M12 4 2.5 20h19z"/><path d="M12 10v4M12 17h.01"/>',
  lock: '<rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  zap: '<path d="M13 3 5 13h6l-1 8 8-10h-6z"/>',
  hash: '<path d="M5 9h14M5 15h14M10 4 8 20M16 4l-2 16"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="M20 20l-3.5-3.5"/>',
  sparkle: '<path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z"/>',
  dot: '<circle cx="12" cy="12" r="4"/>',
  link: '<path d="M9 15l6-6"/><path d="M11 7l1-1a3.5 3.5 0 0 1 5 5l-1 1M13 17l-1 1a3.5 3.5 0 0 1-5-5l1-1"/>',
  cpu: '<rect x="7" y="7" width="10" height="10" rx="1.5"/><path d="M10 3v2M14 3v2M10 19v2M14 19v2M3 10h2M3 14h2M19 10h2M19 14h2"/>',
  branch: '<circle cx="6" cy="6" r="2.5"/><circle cx="6" cy="18" r="2.5"/><circle cx="18" cy="9" r="2.5"/><path d="M6 8.5v7M6 14a6 6 0 0 1 6-6h3.5"/>',
  reply: '<path d="M10 8 5 13l5 5"/><path d="M5 13h9a5 5 0 0 1 5 5v1"/>',
  at: '<circle cx="12" cy="12" r="4"/><path d="M16 8v5a3 3 0 0 0 5 0v-1a9 9 0 1 0-3.5 7"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>',
  pause: '<path d="M9 5v14M15 5v14"/>',
  brain: '<path d="M9 4a3 3 0 0 0-3 3 3 3 0 0 0-1 5.5A3 3 0 0 0 7 18a3 3 0 0 0 5 .5 3 3 0 0 0 5-.5 3 3 0 0 0 2-5.5A3 3 0 0 0 18 7a3 3 0 0 0-3-3 3 3 0 0 0-3 1.5A3 3 0 0 0 9 4z"/>',
  pencil: '<path d="M4 20h4L19 9a2 2 0 0 0-3-3L5 17z"/><path d="M14 7l3 3"/>',
  image: '<rect x="3" y="5" width="18" height="14" rx="2"/><circle cx="9" cy="10" r="1.5"/><path d="M21 15.5l-4.5-4.5L7 19"/>',
  trash: '<path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="M6 7l1 13h10l1-13"/>',
};

export type IconName = keyof typeof ICON_PATHS;

interface IconProps {
  name: IconName | string;
  size?: number;
  strokeWidth?: number;
  className?: string;
  style?: CSSProperties;
  title?: string;
}

export function Icon({ name, size = 18, strokeWidth = 1.7, className, style, title }: IconProps) {
  const d = ICON_PATHS[name];
  if (!d) return null;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      style={style}
      aria-hidden={title ? undefined : true}
      role={title ? "img" : undefined}
      aria-label={title}
      dangerouslySetInnerHTML={{ __html: d }}
    />
  );
}

/**
 * OS brand glyphs (filled, brand-accurate silhouettes) ported from the
 * reference design. Used to badge a Bridge participant with its operating
 * system. Keyed by the normalized OS name (see normalizeOS in lib.ts).
 */
const OS_GLYPHS: Record<string, { label: string; vb: string; path: string }> = {
  mac: {
    label: "macOS",
    vb: "0 0 24 24",
    path: "M17.05 12.04c-.03-2.6 2.12-3.84 2.22-3.9-1.21-1.78-3.1-2.02-3.76-2.05-1.6-.16-3.12.94-3.93.94-.81 0-2.05-.92-3.38-.89-1.74.03-3.34 1.01-4.23 2.57-1.81 3.14-.46 7.78 1.29 10.33.86 1.25 1.88 2.65 3.22 2.6 1.29-.05 1.78-.83 3.34-.83 1.56 0 2 .83 3.38.81 1.4-.03 2.28-1.27 3.13-2.53.99-1.44 1.4-2.84 1.42-2.91-.03-.02-2.72-1.05-2.75-4.12zM14.5 4.74c.71-.86 1.19-2.06 1.06-3.25-1.02.04-2.27.68-3 1.54-.66.76-1.23 1.98-1.08 3.15 1.14.09 2.31-.58 3.02-1.44z",
  },
  windows: {
    label: "Windows",
    vb: "0 0 24 24",
    path: "M3 5.46 10.3 4.4v6.94H3zM11.2 4.27 21 3v8.26h-9.8zM3 12.24h7.3v6.94L3 18.13zM11.2 12.24H21V21l-9.8-1.28z",
  },
  linux: {
    label: "Linux",
    vb: "0 0 24 24",
    path: "M12.5 2c-1.9 0-3.2 1.6-3.2 3.7 0 .9.1 1.3.1 2-.5.7-1.9 2.4-2.7 4.8-.5 1.5-.6 2.2-1.3 3-.5.6-1.2 1-1.2 1.6 0 .5.5.6 1 .5.3.9.2 2.1 1.1 2.1.5 0 .7-.4.8-.8 1.3.5 3 .8 4.9.8s3.6-.3 4.9-.8c.1.4.3.8.8.8.9 0 .8-1.2 1.1-2.1.5.1 1-.1 1-.5 0-.6-.7-1-1.2-1.6-.7-.8-.8-1.5-1.3-3-.8-2.4-2.2-4.1-2.7-4.8 0-.7.1-1.1.1-2C15.7 3.6 14.4 2 12.5 2zm-1.6 4.1c.4 0 .7.4.7.9s-.3.9-.7.9-.7-.4-.7-.9.3-.9.7-.9zm3.2 0c.4 0 .7.4.7.9s-.3.9-.7.9-.7-.4-.7-.9.3-.9.7-.9zm-1.6 2.4c.7 0 1.7.5 1.7 1 0 .3-.4.5-.8.7-.3.2-.7.5-.9.5s-.6-.3-.9-.5c-.4-.2-.8-.4-.8-.7 0-.5 1-1 1.7-1z",
  },
};

export type OSName = keyof typeof OS_GLYPHS;

interface OsGlyphProps {
  os: string;
  size?: number;
  title?: string;
  className?: string;
  style?: CSSProperties;
}

export function OsGlyph({ os, size = 14, title, className, style }: OsGlyphProps) {
  const g = OS_GLYPHS[os];
  if (!g) return null;
  return (
    <svg
      width={size}
      height={size}
      viewBox={g.vb}
      fill="currentColor"
      className={className}
      style={style}
      role="img"
      aria-label={title || g.label}
      dangerouslySetInnerHTML={{ __html: `<path d="${g.path}"/>` }}
    />
  );
}

export function osLabel(os: string): string {
  return OS_GLYPHS[os]?.label || os;
}
