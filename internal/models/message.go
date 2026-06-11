package models

import "time"

type MessageType string

const (
	MessageTypeChat          MessageType = "chat"
	MessageTypeCommand       MessageType = "command"
	MessageTypeCommandResult MessageType = "command_result"
	MessageTypePresence      MessageType = "presence"
	MessageTypeSystem        MessageType = "system"
	MessageTypeTrace         MessageType = "trace"
	// MessageTypeControl carries an out-of-band signal to a participant rather
	// than conversation. The relay treats it as opaque and routes it by
	// target_id like any other message; the recipient interprets
	// metadata.operation (e.g. "stop" to interrupt an in-flight reply).
	MessageTypeControl MessageType = "control"
)

// Control operations carried in a MessageTypeControl message's
// metadata["operation"].
const (
	ControlOperationStop = "stop"
	// ControlOperationPermissionReply 回灌一次审批决策给在飞 generation。
	// 携带 metadata: permission_id(对应出站 trace 的 permission_id)、reply
	// (值为 allow_once|allow_always|deny)。由 bridge readLoop 按 permission_id
	// 路由到等待中的 RequestPermission 闭包。
	ControlOperationPermissionReply = "permission_reply"
	// ControlOperationConfigUpdate pushes server-managed startup config
	// (model / api_base_url / api_key) to a single targeted agent. It is
	// delivered ONLY over that agent's own connection (never broadcast, never
	// persisted to room history) and carries the api key in plaintext on that
	// targeted channel — handlers must redact metadata["api_key"] before any
	// logging. The bridge applies it to its provider's in-memory runtime
	// config; an empty field means "fall back to the bridge local default".
	ControlOperationConfigUpdate = "config_update"

	// ControlOperationJoinRoom is sent by the relay over the control connection
	// to instruct the bridge to join (or remain in) a given room.
	// metadata["room_id"] names the target room.
	ControlOperationJoinRoom = "join_room"

	// ControlOperationLeaveRoom is sent by the relay over the control connection
	// to instruct the bridge to leave a given room.
	// metadata["room_id"] names the target room.
	ControlOperationLeaveRoom = "leave_room"

	// ControlOperationRoomStateReport is sent by the bridge to the relay over
	// the control connection to report the current set of connected rooms.
	// metadata["rooms"] is a comma-separated list of room ids.
	ControlOperationRoomStateReport = "room_state_report"
)

type SenderKind string

const (
	SenderKindUser   SenderKind = "user"
	SenderKindAgent  SenderKind = "agent"
	SenderKindSystem SenderKind = "system"
)

// ChatMessage is the network envelope shared by relay clients.
type ChatMessage struct {
	ID             string            `json:"id"`
	RoomID         string            `json:"room_id"`
	Type           MessageType       `json:"type"`
	SenderID       string            `json:"sender_id"`
	SenderKind     SenderKind        `json:"sender_kind"`
	TargetID       string            `json:"target_id,omitempty"`
	Content        string            `json:"content"`
	ReplyRequested bool              `json:"reply_requested"`
	TurnBudget     int               `json:"turn_budget"`
	CreatedAt      time.Time         `json:"created_at"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

func (m ChatMessage) IsAddressedTo(agentID string) bool {
	return m.TargetID == "" || m.TargetID == agentID
}

type Participant struct {
	ID              string                  `json:"id"`
	RoomID          string                  `json:"room_id"`
	Kind            SenderKind              `json:"kind"`
	Label           string                  `json:"label"`
	ConnectionID    string                  `json:"connection_id,omitempty"`
	ConnectionCount int                     `json:"connection_count,omitempty"`
	Connections     []ParticipantConnection `json:"connections,omitempty"`
	RemoteAddr      string                  `json:"remote_addr,omitempty"`
	ConnectedAt     time.Time               `json:"connected_at"`
	LastSeenAt      time.Time               `json:"last_seen_at"`
	Metadata        map[string]string       `json:"metadata,omitempty"`
}

type ParticipantConnection struct {
	ID          string            `json:"id"`
	Label       string            `json:"label,omitempty"`
	RemoteAddr  string            `json:"remote_addr,omitempty"`
	ConnectedAt time.Time         `json:"connected_at"`
	LastSeenAt  time.Time         `json:"last_seen_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}
