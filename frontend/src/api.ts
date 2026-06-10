import type {
  AccessPersistence,
  AccessRequest,
  AttachmentUpload,
  ChatMessage,
  Me,
  Participant,
  Room,
  RoomSummary,
} from "./types";
import { cleanRoom } from "./lib";

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`${path} → ${res.status}`);
  return res.json() as Promise<T>;
}

async function sendJSON<T>(path: string, method: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`${path} ${method} → ${res.status}`);
  return res.json() as Promise<T>;
}

export async function loadMe(): Promise<Me> {
  try {
    return await getJSON<Me>("/v1/me");
  } catch {
    // Server doesn't ship the endpoint yet → treat auth as disabled, keep app working.
    return { authenticated: false, auth_enabled: false };
  }
}

export async function logout(): Promise<void> {
  try {
    await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
  } catch {
    // best effort
  }
}

export async function createRoom(title?: string): Promise<Room> {
  return sendJSON<Room>("/v1/rooms", "POST", title ? { title } : {});
}

// listAllRooms returns one page of rooms newest-first (admin-only on the
// server). Resolves to an empty list when the caller isn't an admin or the
// endpoint isn't available, so the UI can call it unconditionally.
export async function listAllRooms(offset = 0, limit = 50): Promise<Room[]> {
  try {
    return await getJSON<Room[]>(`/v1/admin/rooms?limit=${limit}&offset=${offset}`);
  } catch {
    return [];
  }
}

export async function getRoom(roomID: string): Promise<Room | null> {
  try {
    return await getJSON<Room>(`/v1/rooms/${encodeURIComponent(cleanRoom(roomID))}`);
  } catch {
    return null;
  }
}

export async function patchRoom(
  roomID: string,
  patch: Partial<Pick<Room, "title" | "gated" | "ended">>,
): Promise<Room> {
  return sendJSON<Room>(`/v1/rooms/${encodeURIComponent(cleanRoom(roomID))}`, "PATCH", patch);
}

export async function deleteRoom(roomID: string): Promise<void> {
  const res = await fetch(`/v1/rooms/${encodeURIComponent(cleanRoom(roomID))}`, {
    method: "DELETE",
    credentials: "same-origin",
  });
  if (!res.ok) throw new Error(`delete room → ${res.status}`);
}

export async function loadHistory(roomID: string): Promise<ChatMessage[]> {
  const res = await fetch(`/v1/rooms/${encodeURIComponent(roomID)}/messages?limit=240`);
  if (!res.ok) throw new Error("history request failed");
  return res.json();
}

// loadHistoryBefore pages backwards into older history: it returns up to
// `limit` messages stored before `beforeID` (oldest-first), the cursor being
// the id of the oldest message currently held. An empty array means there is
// nothing older — the top of the room.
export async function loadHistoryBefore(
  roomID: string,
  beforeID: string,
  limit = 120,
): Promise<ChatMessage[]> {
  const params = new URLSearchParams({ before: beforeID, limit: String(limit) });
  const res = await fetch(
    `/v1/rooms/${encodeURIComponent(roomID)}/messages?${params.toString()}`,
  );
  if (!res.ok) throw new Error("history page request failed");
  return res.json();
}

export async function loadParticipants(roomID: string): Promise<Participant[]> {
  const res = await fetch(`/v1/rooms/${encodeURIComponent(roomID)}/participants`);
  if (!res.ok) throw new Error("participants request failed");
  return res.json();
}

export async function postMessage(roomID: string, message: ChatMessage): Promise<void> {
  const res = await fetch(`/v1/rooms/${encodeURIComponent(roomID)}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(message),
  });
  if (!res.ok) throw new Error("send failed");
}

// uploadAttachment posts one image to the room's attachment store. The server
// sniffs the real mime (raster images only) and enforces size/quota caps, so
// a non-2xx here surfaces as a thrown error with the server's reason.
export async function uploadAttachment(roomID: string, file: File): Promise<AttachmentUpload> {
  const form = new FormData();
  form.append("file", file, file.name);
  const res = await fetch(`/v1/rooms/${encodeURIComponent(cleanRoom(roomID))}/attachments`, {
    method: "POST",
    body: form,
  });
  if (!res.ok) {
    let reason = `upload → ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) reason = body.error;
    } catch {
      // keep status fallback
    }
    throw new Error(reason);
  }
  return res.json() as Promise<AttachmentUpload>;
}

// attachmentURL builds the download path for an attachment reference.
export function attachmentURL(roomID: string, attachmentID: string): string {
  return `/v1/rooms/${encodeURIComponent(cleanRoom(roomID))}/attachments/${encodeURIComponent(attachmentID)}`;
}

// loadSummary fetches the room's rolling LLM summary. The endpoint always
// returns 200 (an empty RoomSummary when the feature is off or no summary has
// been generated yet), so a null here means the request itself failed.
export async function loadSummary(roomID: string): Promise<RoomSummary | null> {
  try {
    return await getJSON<RoomSummary>(
      `/v1/rooms/${encodeURIComponent(cleanRoom(roomID))}/summary`,
    );
  } catch {
    return null;
  }
}

/* ── access requests ─────────────────────────────────────────────── */

export async function listAccessRequests(roomID: string): Promise<AccessRequest[]> {
  try {
    return await getJSON<AccessRequest[]>(
      `/v1/rooms/${encodeURIComponent(roomID)}/access-requests`,
    );
  } catch {
    return [];
  }
}

export async function createAccessRequest(
  roomID: string,
  label?: string,
): Promise<AccessRequest> {
  return sendJSON<AccessRequest>(
    `/v1/rooms/${encodeURIComponent(roomID)}/access-requests`,
    "POST",
    label ? { label } : {},
  );
}

export async function decideAccessRequest(
  roomID: string,
  requestID: string,
  decision: "approve" | "deny",
  persistence?: AccessPersistence,
): Promise<AccessRequest> {
  return sendJSON<AccessRequest>(
    `/v1/rooms/${encodeURIComponent(roomID)}/access-requests/${encodeURIComponent(requestID)}`,
    "PATCH",
    persistence ? { decision, persistence } : { decision },
  );
}
