export type MessageType = "chat" | "command" | "command_result" | "presence" | "system" | "trace" | "control";
export type SenderKind = "user" | "agent" | "system";

export interface ChatMessage {
  id?: string;
  room_id: string;
  type: MessageType;
  sender_id: string;
  sender_kind: SenderKind;
  target_id?: string;
  content: string;
  reply_requested: boolean;
  turn_budget: number;
  created_at?: string;
  metadata?: Record<string, string>;
}

export interface Participant {
  id: string;
  room_id: string;
  kind: SenderKind;
  label: string;
  connection_id?: string;
  connection_count?: number;
  connections?: ParticipantConnection[];
  remote_addr?: string;
  connected_at: string;
  last_seen_at: string;
  metadata?: Record<string, string>;
}

export interface ParticipantConnection {
  id: string;
  label?: string;
  remote_addr?: string;
  connected_at: string;
  last_seen_at: string;
  metadata?: Record<string, string>;
}

/* ── Attachments (房间图片附件) ──────────────────────────────────── */

// AttachmentRef 是消息 metadata["attachments"] 里携带的附件引用(JSON 数组,
// 与 Go 侧 models.AttachmentRef 对偶)。字节走 /v1/rooms/:id/attachments/:aid。
export interface AttachmentRef {
  id: string;
  mime?: string;
  size?: number;
  name?: string;
}

// AttachmentUpload 是 POST /v1/rooms/:id/attachments 的响应体。
export interface AttachmentUpload {
  id: string;
  room_id: string;
  mime: string;
  size: number;
  url: string;
  created_at: string;
}

/* ── Auth ───────────────────────────────────────────────────────── */

export interface AuthUser {
  login: string;
  name: string;
  email: string;
  avatar_url: string;
}

export type Me =
  | { authenticated: false; auth_enabled: false }
  | { authenticated: false; auth_enabled: true; login_url: string; auth_provider?: "sso" | "github" }
  | { authenticated: true; auth_enabled: true; user: AuthUser; is_admin?: boolean };

/* ── Rooms ──────────────────────────────────────────────────────── */

export interface Room {
  room_id: string;
  owner: string | null;
  title: string | null;
  gated: boolean;
  ended: boolean;
  created_at: string;
}

/* ── Room summary (LLM 滚动摘要) ─────────────────────────────────── */

export interface RoomSummary {
  room_id: string;
  summary: string;
  covered_seq: number;
  // RFC3339 timestamp of when the summary was last regenerated; the zero
  // value ("0001-01-01T00:00:00Z") / null means it has never run.
  updated_at: string | null;
}

/* ── Access requests ────────────────────────────────────────────── */

export type AccessStatus = "pending" | "approved" | "denied";
export type AccessPersistence = "once" | "persist" | null;

export interface AccessRequest {
  id: string;
  room_id: string;
  requester_login: string | null;
  requester_label: string;
  via: string;
  location: string | null;
  status: AccessStatus;
  persistence?: AccessPersistence;
  created_at: string;
  resolved_at?: string | null;
}

export interface AccessDecisionEvent {
  type: "access_decision";
  request_id: string;
  status: "approved" | "denied";
  persistence: AccessPersistence;
}

/* ── Theme ──────────────────────────────────────────────────────── */

export type ThemeMode = "paper" | "operator" | "signal";
export type Density = "compact" | "regular" | "comfy";
