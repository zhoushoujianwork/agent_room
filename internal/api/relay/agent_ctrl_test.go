package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"agent-room/internal/models"
)

// dialCtrlWS connects to /v1/agents/ws with the given token and agentID.
func dialCtrlWS(t *testing.T, wsBase, token, agentID string) *websocket.Conn {
	t.Helper()
	u := wsBase + "/v1/agents/ws?agent_token=" + token + "&client_id=" + agentID
	conn, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("ctrl ws dial: status=%d err=%v", resp.StatusCode, err)
		}
		t.Fatalf("ctrl ws dial: %v", err)
	}
	return conn
}

// readUntilOp reads frames until a control message with the given operation arrives.
func readUntilOp(t *testing.T, conn *websocket.Conn, op string) (models.ChatMessage, bool) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var msg models.ChatMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return models.ChatMessage{}, false
		}
		if msg.Type == models.MessageTypeControl && msg.Metadata["operation"] == op {
			return msg, true
		}
	}
}

// --- Test 1: control connection auth and join replay ---

func TestAgentCtrlWS_AuthAndJoinReplay(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	ctx := context.Background()

	// Pre-seed a desired room for agent-ctrl-1.
	_ = s.agents.AddAgentRoom(ctx, "agent-ctrl-1", "room-abc", "alice")

	srv := httptest.NewServer(s.Routes())
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	// No token → 401.
	_, resp, err := websocket.DefaultDialer.Dial(wsBase+"/v1/agents/ws?client_id=agent-ctrl-1", nil)
	if err == nil {
		t.Fatal("expected dial failure for missing token")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got resp=%v err=%v", resp, err)
	}

	// Valid token → connection success + join_room replayed.
	token, _ := createTokenForUser(t, s, "alice")
	conn := dialCtrlWS(t, wsBase, token, "agent-ctrl-1")
	defer conn.Close()

	msg, ok := readUntilOp(t, conn, models.ControlOperationJoinRoom)
	if !ok {
		t.Fatal("did not receive join_room for seeded desired room")
	}
	if msg.Metadata["room_id"] != "room-abc" {
		t.Fatalf("join_room room_id = %q, want room-abc", msg.Metadata["room_id"])
	}
}

// Test: control connection delivers stored config on connect.
func TestAgentCtrlWS_ConfigPushedOnConnect(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	ctx := context.Background()

	// Pre-upsert agent and config.
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "agent-cfg-1", OwnerLogin: "alice"})
	_ = putConfig(t, s, "alice", "agent-cfg-1", `{"model":"gpt-4o","api_base_url":"https://api/v1"}`)

	srv := httptest.NewServer(s.Routes())
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	token, _ := createTokenForUser(t, s, "alice")
	conn := dialCtrlWS(t, wsBase, token, "agent-cfg-1")
	defer conn.Close()

	msg, ok := readUntilOp(t, conn, models.ControlOperationConfigUpdate)
	if !ok {
		t.Fatal("did not receive config_update on ctrl connect")
	}
	if msg.Metadata["model"] != "gpt-4o" {
		t.Fatalf("config_update model = %q, want gpt-4o", msg.Metadata["model"])
	}
}

// --- Test 2: POST join ---

func TestRoomAgents_PostJoin(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	ctx := context.Background()
	httpBase := httptest.NewServer(s.Routes())
	defer httpBase.Close()
	wsBase := "ws" + strings.TrimPrefix(httpBase.URL, "http")

	// Create rooms.
	openRoom := createTestRoom(t, s, "alice", false)
	gatedRoom := createTestRoom(t, s, "alice", true)

	// Helper: POST join.
	postJoin := func(login, agentID, roomID string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"agent_id": agentID})
		req := httptest.NewRequest(http.MethodPost, "/v1/rooms/"+roomID+"/agents", strings.NewReader(string(body)))
		req.AddCookie(signedCookie(t, agentTestSecret, login))
		rr := httptest.NewRecorder()
		s.Routes().ServeHTTP(rr, req)
		return rr
	}

	// Pre-register alice's agent.
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "agent-join-1", OwnerLogin: "alice"})

	// Non-owner cannot join their own agent to a room? → non-owner of the AGENT (bob).
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "agent-bob-1", OwnerLogin: "bob"})
	rr := postJoin("alice", "agent-bob-1", openRoom)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-owner join = %d, want 403", rr.Code)
	}

	// Gated room: non-room-owner cannot add agent.
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "agent-carol-1", OwnerLogin: "carol"})
	rr = postJoin("carol", "agent-carol-1", gatedRoom)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("gated non-owner join = %d, want 403", rr.Code)
	}

	// Owner joins own agent to open room, agent offline → delivered=false.
	rr = postJoin("alice", "agent-join-1", openRoom)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner join = %d, body=%s", rr.Code, rr.Body.String())
	}
	var result map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&result)
	if result["ok"] != true {
		t.Fatalf("expected ok=true, got %v", result)
	}
	if result["delivered"] != false {
		t.Fatalf("expected delivered=false (agent offline), got %v", result["delivered"])
	}
	// Verify persisted.
	rooms, err := s.agents.ListAgentRooms(ctx, "agent-join-1")
	if err != nil || len(rooms) == 0 {
		t.Fatalf("room not persisted: %v %v", rooms, err)
	}

	// Owner joins own agent — agent online → delivered=true.
	token, _ := createTokenForUser(t, s, "alice")
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "agent-join-2", OwnerLogin: "alice"})
	conn := dialCtrlWS(t, wsBase, token, "agent-join-2")
	defer conn.Close()
	// Give the server a moment to register.
	time.Sleep(50 * time.Millisecond)

	rr2 := postJoin("alice", "agent-join-2", openRoom)
	if rr2.Code != http.StatusOK {
		t.Fatalf("online agent join = %d, body=%s", rr2.Code, rr2.Body.String())
	}
	var result2 map[string]any
	_ = json.NewDecoder(rr2.Body).Decode(&result2)
	if result2["delivered"] != true {
		t.Fatalf("expected delivered=true (agent online), got %v", result2["delivered"])
	}
}

// --- Test 3: DELETE remove ---

func TestRoomAgents_DeleteRemove(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	ctx := context.Background()

	room := createTestRoom(t, s, "alice", false)
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "agent-del-1", OwnerLogin: "alice"})
	_ = s.agents.AddAgentRoom(ctx, "agent-del-1", room, "alice")

	deleteAgent := func(login, agentID, roomID string) int {
		req := httptest.NewRequest(http.MethodDelete, "/v1/rooms/"+roomID+"/agents/"+agentID, nil)
		req.AddCookie(signedCookie(t, agentTestSecret, login))
		rr := httptest.NewRecorder()
		s.Routes().ServeHTTP(rr, req)
		return rr.Code
	}

	// Stranger cannot delete.
	if code := deleteAgent("carol", "agent-del-1", room); code != http.StatusForbidden {
		t.Fatalf("stranger delete = %d, want 403", code)
	}

	// Room owner can delete (alice owns the room AND the agent).
	if code := deleteAgent("alice", "agent-del-1", room); code != http.StatusOK {
		t.Fatalf("owner delete = %d, want 200", code)
	}
	rooms, _ := s.agents.ListAgentRooms(ctx, "agent-del-1")
	if len(rooms) != 0 {
		t.Fatalf("room not removed from desired: %v", rooms)
	}

	// Re-add for admin test.
	_ = s.agents.AddAgentRoom(ctx, "agent-del-1", room, "alice")
	if code := deleteAgent("root", "agent-del-1", room); code != http.StatusOK {
		t.Fatalf("admin delete = %d, want 200", code)
	}
}

// --- Test 4: room_state_report reconciliation ---

func TestRoomStateReport_Reconcile(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	ctx := context.Background()

	// desired = {room-desired}, actual will report {room-actual} only.
	_ = s.agents.AddAgentRoom(ctx, "agent-recon-1", "room-desired", "alice")

	srv := httptest.NewServer(s.Routes())
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	token, _ := createTokenForUser(t, s, "alice")
	conn := dialCtrlWS(t, wsBase, token, "agent-recon-1")
	defer conn.Close()

	// Drain the initial join_room for room-desired.
	_, _ = readUntilOp(t, conn, models.ControlOperationJoinRoom)

	// Send room_state_report: actual = room-actual only (not room-desired).
	report := models.ChatMessage{
		Type:       models.MessageTypeControl,
		SenderKind: models.SenderKindAgent,
		SenderID:   "agent-recon-1",
		Metadata: map[string]string{
			"operation": models.ControlOperationRoomStateReport,
			"rooms":     "room-actual",
		},
	}
	if err := conn.WriteJSON(report); err != nil {
		t.Fatalf("write report: %v", err)
	}

	// desired has room-desired but actual doesn't → should receive join_room for room-desired.
	msg, ok := readUntilOp(t, conn, models.ControlOperationJoinRoom)
	if !ok {
		t.Fatal("expected join_room for missing desired room")
	}
	if msg.Metadata["room_id"] != "room-desired" {
		t.Fatalf("join_room room_id = %q, want room-desired", msg.Metadata["room_id"])
	}

	// actual has room-actual not in desired → absorbed (no leave_room expected, just wait).
	time.Sleep(100 * time.Millisecond)
	rooms, _ := s.agents.ListAgentRooms(ctx, "agent-recon-1")
	found := false
	for _, r := range rooms {
		if r == "room-actual" {
			found = true
		}
	}
	if !found {
		t.Fatalf("room-actual not absorbed into desired: %v", rooms)
	}
}

// --- Test 5: ctrl messages not persisted ---

func TestCtrlMessagesNotPersisted(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	ctx := context.Background()

	// Create a room so we can check its history.
	room := createTestRoom(t, s, "alice", false)

	srv := httptest.NewServer(s.Routes())
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	token, _ := createTokenForUser(t, s, "alice")
	conn := dialCtrlWS(t, wsBase, token, "agent-nopersist-1")
	defer conn.Close()

	// Send room_state_report claiming to be in our test room.
	report := models.ChatMessage{
		Type:       models.MessageTypeControl,
		SenderKind: models.SenderKindAgent,
		SenderID:   "agent-nopersist-1",
		Metadata: map[string]string{
			"operation": models.ControlOperationRoomStateReport,
			"rooms":     room,
		},
	}
	if err := conn.WriteJSON(report); err != nil {
		t.Fatalf("write report: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Room history must be empty.
	history, err := s.service.List(ctx, room, 100)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	for _, h := range history {
		if h.Type == models.MessageTypeControl {
			t.Fatalf("ctrl message leaked into room history: %+v", h)
		}
	}
	if len(history) != 0 {
		t.Fatalf("expected empty history, got %d messages", len(history))
	}
}

// createTestRoom creates a room owned by login directly via the store and
// returns its ID. Uses the store directly to support setting gated=true,
// which the HTTP create-room handler does not expose.
func createTestRoom(t *testing.T, s *Server, owner string, gated bool) string {
	t.Helper()
	ctx := context.Background()
	roomID := "test-room-" + owner
	if gated {
		roomID += "-gated"
	}
	ownerCopy := owner
	room := models.Room{
		ID:        roomID,
		OwnerLogin: &ownerCopy,
		Gated:     gated,
		Ended:     false,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.rooms.CreateRoom(ctx, room); err != nil {
		t.Fatalf("create test room: %v", err)
	}
	return roomID
}
