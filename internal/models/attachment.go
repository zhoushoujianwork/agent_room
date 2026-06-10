package models

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Attachment 是房间内的一份二进制附件(目前只有图片)。字节存平台(relay)侧,
// 生命周期与房间绑定:房间删除时随消息一起级联清理,没有独立 TTL。
// Bytes 不参与 JSON 编码 —— 列表/上传响应只回元信息,字节走专门的下载端点。
type Attachment struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	Bytes     []byte    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// AttachmentStore 持久化房间附件。SQLite 与 in-memory 两套后端都实现它,
// 与其余 store 接口的约定一致。
type AttachmentStore interface {
	PutAttachment(ctx context.Context, att Attachment) error
	// GetAttachment 以 (roomID, id) 双键查找:附件 URL 永远挂在房间路径下,
	// 拿着 id 但不知道房间 URL 的人取不到字节(房间 URL 即访问边界)。
	GetAttachment(ctx context.Context, roomID, id string) (*Attachment, error)
	DeleteAttachmentsByRoom(ctx context.Context, roomID string) error
	// AttachmentRoomUsage 返回房间当前占用的总字节数,relay 用它做每房间配额。
	AttachmentRoomUsage(ctx context.Context, roomID string) (int64, error)
}

// AttachmentRef 是消息 metadata["attachments"] 里携带的附件引用(不含字节)。
// 客户端用 room id + ID 拼下载 URL;MIME/Name 供渲染与提示。
type AttachmentRef struct {
	ID   string `json:"id"`
	MIME string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
	Name string `json:"name,omitempty"`
}

// MetadataKeyAttachments 是 ChatMessage.Metadata 里附件引用列表的键,
// 值为 EncodeAttachmentList 产出的 JSON 数组。
const MetadataKeyAttachments = "attachments"

// EncodeAttachmentList 把附件引用编码进单个 metadata 字符串(JSON 数组)。
func EncodeAttachmentList(refs []AttachmentRef) string {
	if len(refs) == 0 {
		return ""
	}
	buf, err := json.Marshal(refs)
	if err != nil {
		return ""
	}
	return string(buf)
}

// DecodeAttachmentList 解析 EncodeAttachmentList 的输出。容忍空串/坏 JSON,
// 返回 nil 而不是错误 —— metadata 是不可信输入。
func DecodeAttachmentList(raw string) []AttachmentRef {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var refs []AttachmentRef
	if err := json.Unmarshal([]byte(trimmed), &refs); err != nil {
		return nil
	}
	out := refs[:0]
	for _, ref := range refs {
		if strings.TrimSpace(ref.ID) != "" {
			out = append(out, ref)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
