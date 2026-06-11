package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"agent-room/internal/models"
)

// readUntilControl reads frames until a config_update control message arrives or
// the deadline passes. Returns the message and whether one was seen.
func readUntilControl(t *testing.T, conn *websocket.Conn) (models.ChatMessage, bool) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var msg models.ChatMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return models.ChatMessage{}, false
		}
		if msg.Type == models.MessageTypeControl &&
			strings.TrimSpace(msg.Metadata["operation"]) == models.ControlOperationConfigUpdate {
			return msg, true
		}
	}
}

func TestConfigUpdatePushedOnPutAndBind(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	token, _ := createTokenForUser(t, s, "alice")

	srv := httptest.NewServer(s.Routes())
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	conn, _, err := websocket.DefaultDialer.Dial(wsBase+"/v1/rooms/demo/ws?agent_token="+token, nil)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer conn.Close()

	// Presence binds the agent (owner alice) and triggers an initial push only
	// if config exists; here none exists yet, so no control should arrive on bind.
	if err := conn.WriteJSON(models.ChatMessage{
		ID: "m1", RoomID: "demo", Type: models.MessageTypePresence,
		SenderID: "agent-alice-1", SenderKind: models.SenderKindAgent,
		Metadata: map[string]string{"provider": "claude"},
	}); err != nil {
		t.Fatalf("write presence: %v", err)
	}

	// Wait for the bind to land.
	for range 50 {
		if a, err := s.agents.GetAgent(context.Background(), "agent-alice-1"); err == nil && a != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// PUT config → the online agent must receive a config_update with the
	// plaintext key on its own channel (criteria 1/2).
	rr := putConfig(t, s, "alice", "agent-alice-1",
		`{"model":"claude-sonnet-4-6","api_base_url":"https://gw/v1","api_key":"sk-ant-1234567890abcd"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("put = %d, body=%s", rr.Code, rr.Body.String())
	}

	msg, ok := readUntilControl(t, conn)
	if !ok {
		t.Fatalf("agent never received config_update")
	}
	if msg.TargetID != "agent-alice-1" {
		t.Fatalf("target = %q, want agent-alice-1", msg.TargetID)
	}
	if msg.Metadata["model"] != "claude-sonnet-4-6" || msg.Metadata["api_base_url"] != "https://gw/v1" {
		t.Fatalf("config_update metadata = %+v", msg.Metadata)
	}
	if msg.Metadata["api_key"] != "sk-ant-1234567890abcd" {
		t.Fatalf("api_key not delivered on targeted channel: %q", msg.Metadata["api_key"])
	}

	// Criterion 5: the control message must NOT be persisted to room history.
	history, err := s.service.List(context.Background(), "demo", 100)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	for _, h := range history {
		if h.Type == models.MessageTypeControl {
			t.Fatalf("config_update leaked into room history: %+v", h)
		}
		if strings.Contains(h.Content, "sk-ant-1234567890abcd") {
			t.Fatalf("api key leaked into history content")
		}
		for k, v := range h.Metadata {
			if strings.Contains(v, "sk-ant-1234567890abcd") {
				t.Fatalf("api key leaked into history metadata %q", k)
			}
		}
	}
}
