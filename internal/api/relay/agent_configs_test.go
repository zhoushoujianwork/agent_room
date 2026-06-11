package relay

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-room/internal/config"
	"agent-room/internal/io/memory"
	"agent-room/internal/models"
	"agent-room/internal/service/chat"
)

// newConfigServer builds a relay with GitHub session auth, an admin, and an
// optional AGENT_ROOM_SECRET_KEY so the config endpoints are fully wired.
func newConfigServer(t *testing.T, sessionSecret, secretKey string, admins ...string) *Server {
	t.Helper()
	store := memory.NewStore()
	cfg := config.Config{
		GitHub: config.GitHubConfig{
			ClientID: "id", ClientSecret: "sec", SessionSecret: sessionSecret, CookieName: "agent_room_session",
		},
		Admins:    admins,
		SecretKey: secretKey,
	}
	return NewServer(cfg, chat.NewService(store), store, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func putConfig(t *testing.T, s *Server, login, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v1/agents/"+agentID+"/config", strings.NewReader(body))
	req.AddCookie(signedCookie(t, agentTestSecret, login))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	return rr
}

func getConfig(t *testing.T, s *Server, login, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentID+"/config", nil)
	req.AddCookie(signedCookie(t, agentTestSecret, login))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	return rr
}

func TestAgentConfigPutGetMasksKey(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	ctx := context.Background()
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "a1", OwnerLogin: "alice"})

	rr := putConfig(t, s, "alice", "a1",
		`{"model":"claude-sonnet-4-6","api_base_url":"https://gw.example/v1","api_key":"sk-ant-1234567890abcd"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("put = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Stored ciphertext must not contain the plaintext (criterion 3).
	stored, err := s.agents.GetAgentConfig(ctx, "a1")
	if err != nil {
		t.Fatalf("get stored: %v", err)
	}
	if stored.APIKeyCipher == "" || strings.Contains(stored.APIKeyCipher, "sk-ant-1234567890abcd") {
		t.Fatalf("api key not encrypted at rest: %q", stored.APIKeyCipher)
	}

	// GET returns masked key, never plaintext.
	rr = getConfig(t, s, "alice", "a1")
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d, body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "sk-ant-1234567890abcd") {
		t.Fatalf("GET leaked plaintext key: %s", rr.Body.String())
	}
	var resp agentConfigResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Model != "claude-sonnet-4-6" || resp.APIBaseURL != "https://gw.example/v1" {
		t.Fatalf("unexpected config: %+v", resp)
	}
	if resp.APIKeyMasked != "sk-***abcd" {
		t.Fatalf("masked = %q, want sk-***abcd", resp.APIKeyMasked)
	}
}

func TestAgentConfigPartialUpdatePreservesFields(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret")
	ctx := context.Background()
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "a1", OwnerLogin: "alice"})

	if rr := putConfig(t, s, "alice", "a1", `{"model":"m1","api_key":"sk-ant-1234567890abcd"}`); rr.Code != http.StatusOK {
		t.Fatalf("first put = %d", rr.Code)
	}
	// Update only base_url; model + key must survive.
	if rr := putConfig(t, s, "alice", "a1", `{"api_base_url":"https://x/v1"}`); rr.Code != http.StatusOK {
		t.Fatalf("second put = %d", rr.Code)
	}
	cfg, _ := s.agents.GetAgentConfig(ctx, "a1")
	if cfg.Model != "m1" || cfg.APIBaseURL != "https://x/v1" || cfg.APIKeyCipher == "" {
		t.Fatalf("partial update clobbered fields: %+v", cfg)
	}
	// Explicit empty string clears the key.
	if rr := putConfig(t, s, "alice", "a1", `{"api_key":""}`); rr.Code != http.StatusOK {
		t.Fatalf("clear put = %d", rr.Code)
	}
	cfg, _ = s.agents.GetAgentConfig(ctx, "a1")
	if cfg.APIKeyCipher != "" {
		t.Fatalf("empty api_key did not clear cipher: %q", cfg.APIKeyCipher)
	}
}

func TestAgentConfigNonOwnerForbidden(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret", "root")
	ctx := context.Background()
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "a1", OwnerLogin: "alice"})

	// bob is neither owner nor admin (criterion 4).
	if rr := getConfig(t, s, "bob", "a1"); rr.Code != http.StatusForbidden {
		t.Fatalf("bob GET = %d, want 403", rr.Code)
	}
	if rr := putConfig(t, s, "bob", "a1", `{"model":"x"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("bob PUT = %d, want 403", rr.Code)
	}
	// admin may read it.
	if rr := getConfig(t, s, "root", "a1"); rr.Code != http.StatusOK {
		t.Fatalf("admin GET = %d, want 200", rr.Code)
	}
}

func TestAgentConfigUnboundAgent404(t *testing.T) {
	s := newConfigServer(t, agentTestSecret, "the-secret")
	if rr := getConfig(t, s, "alice", "ghost"); rr.Code != http.StatusNotFound {
		t.Fatalf("unbound GET = %d, want 404", rr.Code)
	}
}

func TestAgentConfigAPIKeyRejectedWithoutSecret(t *testing.T) {
	// No secret key configured (criterion 6).
	s := newConfigServer(t, agentTestSecret, "")
	ctx := context.Background()
	_ = s.agents.UpsertAgent(ctx, models.Agent{AgentID: "a1", OwnerLogin: "alice"})

	rr := putConfig(t, s, "alice", "a1", `{"api_key":"sk-ant-1234567890abcd"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("api_key without secret = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	// model/base_url still save fine without a secret.
	if rr := putConfig(t, s, "alice", "a1", `{"model":"m","api_base_url":"https://x/v1"}`); rr.Code != http.StatusOK {
		t.Fatalf("model/base without secret = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	cfg, _ := s.agents.GetAgentConfig(ctx, "a1")
	if cfg.Model != "m" || cfg.APIBaseURL != "https://x/v1" {
		t.Fatalf("model/base not saved: %+v", cfg)
	}
}
