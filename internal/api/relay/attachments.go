package relay

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"agent-room/internal/models"
)

// 附件护栏:单图与每房间总量上限。截图场景字节量很小,超限直接 413 拒绝,
// 不做淘汰 —— 附件生命周期与房间绑定,房间删除时级联清理。
const (
	maxAttachmentBytes  = 5 << 20  // 5 MiB 单图
	roomAttachmentQuota = 50 << 20 // 50 MiB 每房间
)

// allowedAttachmentMIMEs 是允许入库的图片类型,以服务器端嗅探结果为准
// (http.DetectContentType),绝不信客户端报的 Content-Type。只收栅格格式:
// SVG 可携带脚本,放进来就是存储型 XSS,明确拒绝。
var allowedAttachmentMIMEs = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// attachmentUploadResponse 是上传成功的响应体。URL 是相对路径,浏览器直接用;
// bridge 侧拼上自己的 relay HTTP base 得到绝对 URL。
type attachmentUploadResponse struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// parseAttachmentPath matches /v1/rooms/:id/attachments and
// /v1/rooms/:id/attachments/:aid, mirroring parseAccessRequestPath.
func parseAttachmentPath(path string) (string, string, bool) {
	rest := strings.TrimPrefix(path, "/v1/rooms/")
	if rest == path {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 || parts[1] != "attachments" || parts[0] == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		return parts[0], "", true
	}
	if len(parts) == 3 && parts[2] != "" {
		return parts[0], parts[2], true
	}
	return "", "", false
}

func (s *Server) handleAttachments(w http.ResponseWriter, r *http.Request, roomID, attachmentID string) {
	if s.attachments == nil {
		writeError(w, http.StatusNotFound, "attachments not supported")
		return
	}
	switch {
	case attachmentID == "" && r.Method == http.MethodPost:
		s.handleUploadAttachment(w, r, roomID)
	case attachmentID != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.handleGetAttachment(w, r, roomID, attachmentID)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleUploadAttachment 收一张图片入库。授权边界与 POST message 对齐:知道
// 房间 URL 即可上传,不另立一套。接受 multipart/form-data 的 file 字段(浏览
// 器),或裸 body(curl/bridge 直传)。
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request, roomID string) {
	// 防内存炸:无论哪种编码,读到上限即砍。+1KB 容 multipart 边界开销。
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+1024)

	data, err := readAttachmentBody(r)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "attachment too large or unreadable (max 5 MiB)")
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "empty attachment")
		return
	}
	if len(data) > maxAttachmentBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "attachment too large (max 5 MiB)")
		return
	}

	// mime 一律服务器端嗅探,客户端报什么不重要。
	sniffed := http.DetectContentType(data)
	if _, ok := allowedAttachmentMIMEs[sniffed]; !ok {
		writeError(w, http.StatusUnsupportedMediaType, fmt.Sprintf("unsupported attachment type %q (raster images only: png/jpeg/webp/gif)", sniffed))
		return
	}

	usage, err := s.attachments.AttachmentRoomUsage(r.Context(), roomID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if usage+int64(len(data)) > roomAttachmentQuota {
		writeError(w, http.StatusRequestEntityTooLarge, "room attachment quota exceeded (50 MiB)")
		return
	}

	att := models.Attachment{
		ID:        newAttachmentID(),
		RoomID:    roomID,
		MIME:      sniffed,
		Size:      int64(len(data)),
		Bytes:     data,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.attachments.PutAttachment(r.Context(), att); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, attachmentUploadResponse{
		ID:        att.ID,
		RoomID:    att.RoomID,
		MIME:      att.MIME,
		Size:      att.Size,
		URL:       fmt.Sprintf("/v1/rooms/%s/attachments/%s", roomID, att.ID),
		CreatedAt: att.CreatedAt,
	})
}

// readAttachmentBody 读出上传的图片字节:multipart 表单取 file 字段,
// 其余编码当裸 body 整读。
func readAttachmentBody(r *http.Request) ([]byte, error) {
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType == "multipart/form-data" {
		file, _, err := r.FormFile("file")
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return io.ReadAll(file)
	}
	return io.ReadAll(r.Body)
}

// handleGetAttachment 流式吐回附件字节。安全头是硬要求:nosniff + inline,
// 且只有白名单内的栅格 mime 能入库,杜绝把 relay 当任意文件托管/XSS 跳板。
func (s *Server) handleGetAttachment(w http.ResponseWriter, r *http.Request, roomID, attachmentID string) {
	att, err := s.attachments.GetAttachment(r.Context(), roomID, attachmentID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "attachment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ext := allowedAttachmentMIMEs[att.MIME]
	w.Header().Set("Content-Type", att.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s%s"`, att.ID, ext))
	// 附件内容永不变更,房间删除后 404;放心让浏览器长缓存。
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", att.Size))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(att.Bytes)
}

// newAttachmentID returns a 96-bit random hex id, same shape as room ids.
func newAttachmentID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("relay: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b[:])
}
