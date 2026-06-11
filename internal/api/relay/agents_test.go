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

const agentTestSecret = "secret-32-bytes-or-so-here-12345"

// createTokenForUser generates an agent token for login via the HTTP API and
// returns the plaintext + hash prefix.
func createTokenForUser(t *testing.T, s *Server, login string) (token, prefix string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/tokens", strings.NewReader(`{"note":"office"}`))
	req.AddCookie(signedCookie(t, agentTestSecret, login))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create token = %d, body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Token      string `json:"token"`
		HashPrefix string `json:"hash_prefix"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if out.Token == "" || out.HashPrefix == "" {
		t.Fatalf("empty token/prefix: %+v", out)
	}
	return out.Token, out.HashPrefix
}

func TestAgentTokenCreateValidateRevoke(t *testing.T) {
	s := newAdminServer(t, agentTestSecret, "root")
	ctx := context.Background()

	token, prefix := createTokenForUser(t, s, "alice")

	// The plaintext validates to alice.
	owner, ok := s.validateAgentToken(ctx, token)
	if !ok || owner != "alice" {
		t.Fatalf("validate = %q,%v want alice,true", owner, ok)
	}
	// A bogus token does not validate.
	if _, ok := s.validateAgentToken(ctx, "not-a-real-token"); ok {
		t.Fatalf("bogus token validated")
	}

	// Listing shows only the prefix, never the plaintext.
	req := httptest.NewRequest(http.MethodGet, "/v1/agents/tokens", nil)
	req.AddCookie(signedCookie(t, agentTestSecret, "alice"))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list tokens = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), token) {
		t.Fatalf("plaintext token leaked in list response")
	}

	// Revoke by prefix; afterwards the token no longer validates.
	req = httptest.NewRequest(http.MethodDelete, "/v1/agents/tokens/"+prefix, nil)
	req.AddCookie(signedCookie(t, agentTestSecret, "alice"))
	rr = httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke = %d, body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := s.validateAgentToken(ctx, token); ok {
		t.Fatalf("revoked token still validates")
	}
}

func TestAgentTokenRequiresAuth(t *testing.T) {
	s := newAdminServer(t, agentTestSecret, "root")
	// No cookie → 403.
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/tokens", nil)
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("anonymous create = %d, want 403", rr.Code)
	}
}

func TestAgentsListOwnerScopedAndAdmin(t *testing.T) {
	s := newAdminServer(t, agentTestSecret, "root")
	ctx := context.Background()

	// Bind two agents to different owners directly via the store.
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "a-alice", OwnerLogin: "alice"})
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "a-bob", OwnerLogin: "bob"})

	// alice sees only her agent.
	list := getAgents(t, s, "alice")
	if len(list) != 1 || list[0].AgentID != "a-alice" {
		t.Fatalf("alice agents = %+v", list)
	}

	// admin sees all.
	all := getAgents(t, s, "root")
	if len(all) != 2 {
		t.Fatalf("admin agents = %+v", all)
	}

	// alice cannot unbind bob's agent.
	req := httptest.NewRequest(http.MethodDelete, "/v1/agents/a-bob", nil)
	req.AddCookie(signedCookie(t, agentTestSecret, "alice"))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("alice unbind bob = %d, want 403", rr.Code)
	}

	// alice can unbind her own; it then disappears from her list.
	req = httptest.NewRequest(http.MethodDelete, "/v1/agents/a-alice", nil)
	req.AddCookie(signedCookie(t, agentTestSecret, "alice"))
	rr = httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("alice unbind own = %d, body=%s", rr.Code, rr.Body.String())
	}
	if list := getAgents(t, s, "alice"); len(list) != 0 {
		t.Fatalf("unbound agent still listed: %+v", list)
	}
}

func TestWSHandshakeBindsOwnerAndRejectsBadToken(t *testing.T) {
	s := newAdminServer(t, agentTestSecret, "root")
	token, _ := createTokenForUser(t, s, "alice")

	srv := httptest.NewServer(s.Routes())
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Invalid token → handshake rejected with 401.
	_, resp, err := websocket.DefaultDialer.Dial(wsBase+"/v1/rooms/demo/ws?agent_token=bogus", nil)
	if err == nil {
		t.Fatalf("expected bad-token handshake to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad-token handshake status = %v, want 401", resp)
	}

	// Valid token → handshake succeeds; a presence message binds the agent row.
	conn, _, err := websocket.DefaultDialer.Dial(wsBase+"/v1/rooms/demo/ws?agent_token="+token, nil)
	if err != nil {
		t.Fatalf("valid-token handshake failed: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(models.ChatMessage{
		ID:         "m1",
		RoomID:     "demo",
		Type:       models.MessageTypePresence,
		SenderID:   "agent-alice-1",
		SenderKind: models.SenderKindAgent,
		Metadata:   map[string]string{"provider": "claude", "label": "Alice Bot", "owner_login": "evil-spoof"},
	}); err != nil {
		t.Fatalf("write presence: %v", err)
	}

	// The relay binds asynchronously in readLoop; poll briefly.
	var bound *models.Agent
	for range 50 {
		if a, err := s.agents.GetAgent(context.Background(), "agent-alice-1"); err == nil {
			bound = a
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if bound == nil {
		t.Fatalf("agent row was not created from presence")
	}
	if bound.OwnerLogin != "alice" {
		t.Fatalf("owner = %q, want alice (client-supplied owner_login must be ignored)", bound.OwnerLogin)
	}
	if bound.Provider != "claude" || bound.Label != "Alice Bot" {
		t.Fatalf("agent metadata not captured: %+v", bound)
	}

	// Presence over the participants endpoint carries the server-derived owner.
	parts := s.hub.Participants("demo")
	found := false
	for _, p := range parts {
		if p.ID == "agent-alice-1" {
			found = true
			if p.Metadata["owner_login"] != "alice" {
				t.Fatalf("participant owner_login = %q, want alice", p.Metadata["owner_login"])
			}
		}
	}
	if !found {
		t.Fatalf("agent not present in participants")
	}
}

func getAgents(t *testing.T, s *Server, login string) []models.Agent {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	req.AddCookie(signedCookie(t, agentTestSecret, login))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list agents (%s) = %d, body=%s", login, rr.Code, rr.Body.String())
	}
	var out struct {
		Agents []models.Agent `json:"agents"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	return out.Agents
}
