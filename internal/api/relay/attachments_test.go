package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-room/internal/config"
	"agent-room/internal/io/memory"
	"agent-room/internal/service/chat"
)

// minimalPNG is the smallest header http.DetectContentType recognizes as
// image/png. Content beyond the signature doesn't matter for sniffing.
var minimalPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func newAttachmentTestServer() *Server {
	store := memory.NewStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(config.Config{}, chat.NewService(store), store, logger).
		WithAttachments(store)
}

func uploadAttachment(t *testing.T, s *Server, roomID string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/rooms/"+roomID+"/attachments", bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	return rr
}

func TestAttachmentUploadAndDownload(t *testing.T) {
	s := newAttachmentTestServer()

	// 裸 body 上传(curl/bridge 路径)。
	rr := uploadAttachment(t, s, "roomx", minimalPNG, "application/octet-stream")
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp attachmentUploadResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// mime 必须来自服务器端嗅探,不是客户端报的 octet-stream。
	if resp.MIME != "image/png" {
		t.Fatalf("mime = %q, want image/png (sniffed)", resp.MIME)
	}
	if resp.URL != "/v1/rooms/roomx/attachments/"+resp.ID {
		t.Fatalf("url = %q", resp.URL)
	}

	// 下载回来,校验安全头与字节。
	getReq := httptest.NewRequest(http.MethodGet, resp.URL, nil)
	getRR := httptest.NewRecorder()
	s.Routes().ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("download status = %d; body=%s", getRR.Code, getRR.Body.String())
	}
	if got := getRR.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := getRR.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if !bytes.Equal(getRR.Body.Bytes(), minimalPNG) {
		t.Fatal("downloaded bytes differ from upload")
	}

	// 错房间取不到 —— 房间 URL 即访问边界。
	crossReq := httptest.NewRequest(http.MethodGet, "/v1/rooms/other/attachments/"+resp.ID, nil)
	crossRR := httptest.NewRecorder()
	s.Routes().ServeHTTP(crossRR, crossReq)
	if crossRR.Code != http.StatusNotFound {
		t.Fatalf("cross-room download status = %d, want 404", crossRR.Code)
	}
}

func TestAttachmentUploadMultipart(t *testing.T) {
	s := newAttachmentTestServer()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "shot.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(minimalPNG); err != nil {
		t.Fatalf("write part: %v", err)
	}
	_ = mw.Close()

	rr := uploadAttachment(t, s, "roomy", buf.Bytes(), mw.FormDataContentType())
	if rr.Code != http.StatusCreated {
		t.Fatalf("multipart upload status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp attachmentUploadResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MIME != "image/png" || resp.Size != int64(len(minimalPNG)) {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestAttachmentRejectsNonRaster(t *testing.T) {
	s := newAttachmentTestServer()

	cases := map[string][]byte{
		// SVG 可带脚本 → 存储型 XSS,必须拒。
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html": []byte(`<!DOCTYPE html><html><body>hi</body></html>`),
		"text": []byte("just some text, definitely not an image"),
	}
	for name, body := range cases {
		rr := uploadAttachment(t, s, "roomz", body, "image/png") // 谎报 mime 也没用
		if rr.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s upload status = %d, want 415; body=%s", name, rr.Code, rr.Body.String())
		}
	}

	// 空 body。
	rr := uploadAttachment(t, s, "roomz", nil, "image/png")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty upload status = %d, want 400", rr.Code)
	}
}

func TestAttachmentSizeAndQuota(t *testing.T) {
	s := newAttachmentTestServer()

	// 单图超限:PNG 头 + 填充到 5MiB+1。
	big := make([]byte, maxAttachmentBytes+1)
	copy(big, minimalPNG)
	rr := uploadAttachment(t, s, "roomq", big, "image/png")
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload status = %d, want 413", rr.Code)
	}
}

func TestAttachmentRoutesWithoutStore(t *testing.T) {
	store := memory.NewStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer(config.Config{}, chat.NewService(store), store, logger) // no WithAttachments

	rr := uploadAttachment(t, s, "roomn", minimalPNG, "image/png")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("upload without store status = %d, want 404", rr.Code)
	}
}
