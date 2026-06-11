export function cleanRoom(value: string | null | undefined): string {
  return String(value || "")
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "") || "default";
}

/**
 * shortRoomID returns a display-friendly truncation: prefix + first 6 of the
 * random suffix + ellipsis + last 6. Keeps it scannable in chips and lists
 * while preserving enough entropy to recognise the room.
 */
export function shortRoomID(value: string | null | undefined): string {
  const raw = String(value || "").trim();
  if (!raw) return "";
  const dash = raw.indexOf("-");
  const prefix = dash > 0 ? raw.slice(0, dash + 1) : "";
  const body = dash > 0 ? raw.slice(dash + 1) : raw;
  if (body.length <= 14) return raw;
  return `${prefix}${body.slice(0, 6)}…${body.slice(-6)}`;
}

export function makeID(prefix: string): string {
  const bytes = new Uint8Array(6);
  crypto.getRandomValues(bytes);
  return `${prefix}-${Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")}`;
}

export function roomFromLocation(): string {
  const match = window.location.pathname.match(/^\/rooms\/([^/]+)/);
  if (match) return cleanRoom(decodeURIComponent(match[1]));
  const param = new URLSearchParams(window.location.search).get("room");
  return cleanRoom(param || localStorage.getItem("agent-room.room") || "default");
}

export function hasRoomInLocation(): boolean {
  if (window.location.pathname.match(/^\/rooms\/([^/]+)/)) return true;
  return new URLSearchParams(window.location.search).has("room");
}

export function wsBaseURL(room: string): string {
  const scheme = window.location.protocol === "https:" ? "wss" : "ws";
  return `${scheme}://${window.location.host}/v1/rooms/${encodeURIComponent(room)}/ws`;
}

export interface WSIdentity {
  connectionID: string;
  connectionLabel: string;
  principalID: string;
  principalLabel: string;
  principalEmail?: string;
  principalName?: string;
  audit?: boolean;
}

export function wsURL(room: string, identity: WSIdentity): string {
  const params = new URLSearchParams({
    client_id: identity.connectionID,
    client_kind: "user",
    client_label: identity.connectionLabel || identity.connectionID,
    principal_id: identity.principalID,
    principal_label: identity.principalLabel || identity.principalID,
  });
  if (identity.principalEmail) params.set("principal_email", identity.principalEmail);
  if (identity.principalName) params.set("principal_name", identity.principalName);
  if (identity.audit) params.set("audit", "1");
  return `${wsBaseURL(room)}?${params.toString()}`;
}

export function formatTime(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function shortClock(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function relativeTime(value?: string): string {
  if (!value) return "connected";
  const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000));
  if (seconds < 5) return "now";
  if (seconds < 60) return `${seconds}s ago`;
  return `${Math.round(seconds / 60)}m ago`;
}

/**
 * relativeAge is like relativeTime but spans seconds → days, for timestamps
 * that can be much older than a live presence (e.g. when a room summary was
 * last regenerated). Returns "" for an empty/zero/invalid value.
 */
export function relativeAge(value?: string | null): string {
  if (!value) return "";
  const t = new Date(value).getTime();
  if (Number.isNaN(t) || t <= 0) return "";
  const seconds = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (seconds < 10) return "刚刚";
  if (seconds < 60) return `${seconds} 秒前`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.round(hours / 24);
  return `${days} 天前`;
}

/**
 * absoluteTime renders a full local date + time, used as the precise companion
 * to relativeAge (e.g. tooltip / secondary line on the summary card).
 */
export function absoluteTime(value?: string | null): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getTime() <= 0) return "";
  return date.toLocaleString([], {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export async function copyText(text: string): Promise<void> {
  await navigator.clipboard.writeText(text);
}

/**
 * hueFromID derives a deterministic 0..359 hue value from a string. Used to
 * tint avatars / name / activity rails consistently per Bridge participant.
 * Reference design hard-codes a hue per agent in data.jsx; we derive on the
 * client because the backend doesn't ship one.
 */
export function hueFromID(id: string | null | undefined): number {
  const raw = String(id || "");
  if (!raw) return 220;
  // FNV-1a 32-bit hash for stable distribution
  let h = 0x811c9dc5;
  for (let i = 0; i < raw.length; i += 1) {
    h ^= raw.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return Math.abs(h) % 360;
}

export interface AgentTint {
  solid: string;
  bright: string;
  soft: string;
  faint: string;
  line: string;
  text: string;
}

export function agentTint(hue: number, dark: boolean): AgentTint {
  if (dark) {
    return {
      solid: `oklch(0.74 0.15 ${hue})`,
      bright: `oklch(0.82 0.13 ${hue})`,
      soft: `oklch(0.74 0.15 ${hue} / 0.16)`,
      faint: `oklch(0.74 0.15 ${hue} / 0.09)`,
      line: `oklch(0.74 0.15 ${hue} / 0.42)`,
      text: `oklch(0.84 0.11 ${hue})`,
    };
  }
  return {
    solid: `oklch(0.56 0.16 ${hue})`,
    bright: `oklch(0.5 0.17 ${hue})`,
    soft: `oklch(0.56 0.15 ${hue} / 0.12)`,
    faint: `oklch(0.56 0.15 ${hue} / 0.06)`,
    line: `oklch(0.56 0.15 ${hue} / 0.34)`,
    text: `oklch(0.46 0.17 ${hue})`,
  };
}

export function initials(label: string | null | undefined): string {
  const clean = String(label || "?").replace(/^@/, "");
  const parts = clean.split(/[-_.\s]/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return clean.slice(0, 2).toUpperCase();
}

/**
 * normalizeOS maps a raw OS string (runtime.GOOS from the bridge, or a free-form
 * value) to one of the three glyph keys, or null when unknown/absent. Returning
 * null keeps avatars badge-free for older bridges or auth-disabled installs that
 * don't report an OS, rather than rendering an empty badge box.
 */
export function normalizeOS(value: string | null | undefined): "mac" | "windows" | "linux" | null {
  const raw = String(value || "").trim().toLowerCase();
  if (!raw) return null;
  if (raw === "darwin" || raw === "mac" || raw === "macos" || raw === "osx") return "mac";
  if (raw.startsWith("win")) return "windows";
  if (raw === "linux") return "linux";
  return null;
}

export function formatElapsed(ms: number): string {
  if (!ms || !isFinite(ms)) return "0.0s";
  const s = ms / 1000;
  if (s < 10) return `${s.toFixed(1)}s`;
  if (s < 60) return `${Math.round(s)}s`;
  const m = Math.floor(s / 60);
  const r = Math.round(s % 60);
  return `${m}m${r.toString().padStart(2, "0")}s`;
}

export function durationMsLabel(value?: string): string {
  if (!value) return "";
  const ms = Number(value);
  if (!Number.isFinite(ms)) return "";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}
