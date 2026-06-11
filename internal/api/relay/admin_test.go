package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-room/internal/config"
	"agent-room/internal/io/memory"
	"agent-room/internal/models"
	"agent-room/internal/service/chat"
)

// newAdminServer builds an auth-enabled server with the given admin logins.
func newAdminServer(t *testing.T, secret string, admins ...string) *Server {
	t.Helper()
	store := memory.NewStore()
	cfg := config.Config{
		GitHub: config.GitHubConfig{
			ClientID: "id", ClientSecret: "sec", SessionSecret: secret, CookieName: "agent_room_session",
		},
		Admins: admins,
	}
	return NewServer(cfg, chat.NewService(store), store, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestConfigIsAdmin(t *testing.T) {
	cfg := config.Config{Admins: []string{"admin", " Alice "}}
	cases := map[string]bool{
		"admin": true,
		"ADMIN": true, // case-insensitive
		"alice": true, // trimmed entry still matches
		"bob":   false,
		"":      false,
	}
	for login, want := range cases {
		if got := cfg.IsAdmin(login); got != want {
			t.Errorf("IsAdmin(%q) = %v, want %v", login, got, want)
		}
	}
}

func TestAdminCanManageAnyRoom(t *testing.T) {
	secret := "secret-32-bytes-or-so-here-12345"
	s := newAdminServer(t, secret, "root")

	// alice creates a room she owns.
	req := httptest.NewRequest(http.MethodPost, "/v1/rooms", nil)
	req.AddCookie(signedCookie(t, secret, "alice"))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	var room models.Room
	_ = json.NewDecoder(rr.Body).Decode(&room)

	// A non-owner, non-admin (bob) is still rejected.
	payload, _ := json.Marshal(map[string]any{"title": "bob title"})
	req = httptest.NewRequest(http.MethodPatch, "/v1/rooms/"+room.ID, bytes.NewReader(payload))
	req.AddCookie(signedCookie(t, secret, "bob"))
	rr = httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin PATCH = %d, want 403", rr.Code)
	}

	// The admin (root) can PATCH a room they do not own.
	payload, _ = json.Marshal(map[string]any{"title": "admin title"})
	req = httptest.NewRequest(http.MethodPatch, "/v1/rooms/"+room.ID, bytes.NewReader(payload))
	req.AddCookie(signedCookie(t, secret, "root"))
	rr = httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin PATCH = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var updated models.Room
	_ = json.NewDecoder(rr.Body).Decode(&updated)
	if updated.Title == nil || *updated.Title != "admin title" {
		t.Fatalf("title = %v, want admin title", updated.Title)
	}
}

func TestAdminRoomsEndpoint(t *testing.T) {
	secret := "secret-32-bytes-or-so-here-12345"
	s := newAdminServer(t, secret, "root")

	// Seed two rooms owned by different people.
	for _, owner := range []string{"alice", "bob"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/rooms", nil)
		req.AddCookie(signedCookie(t, secret, owner))
		rr := httptest.NewRecorder()
		s.Routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("seed room for %s = %d", owner, rr.Code)
		}
	}

	// Admin sees every room.
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/rooms", nil)
	req.AddCookie(signedCookie(t, secret, "root"))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin list = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var rooms []models.Room
	_ = json.NewDecoder(rr.Body).Decode(&rooms)
	if len(rooms) != 2 {
		t.Fatalf("admin list len = %d, want 2", len(rooms))
	}

	// Pagination: limit=1 returns one room, offset=1 returns the other,
	// and an offset past the end returns an empty list.
	listPage := func(query string) []models.Room {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/rooms"+query, nil)
		req.AddCookie(signedCookie(t, secret, "root"))
		rr := httptest.NewRecorder()
		s.Routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin list %s = %d, want 200; body=%s", query, rr.Code, rr.Body.String())
		}
		var page []models.Room
		_ = json.NewDecoder(rr.Body).Decode(&page)
		return page
	}
	first := listPage("?limit=1")
	if len(first) != 1 {
		t.Fatalf("limit=1 len = %d, want 1", len(first))
	}
	second := listPage("?limit=1&offset=1")
	if len(second) != 1 {
		t.Fatalf("offset=1 len = %d, want 1", len(second))
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("pages overlap: both returned room %s", first[0].ID)
	}
	if empty := listPage("?limit=1&offset=2"); len(empty) != 0 {
		t.Fatalf("offset past end len = %d, want 0", len(empty))
	}

	// Bad pagination params are rejected.
	req = httptest.NewRequest(http.MethodGet, "/v1/admin/rooms?limit=abc", nil)
	req.AddCookie(signedCookie(t, secret, "root"))
	rr = httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad limit = %d, want 400", rr.Code)
	}

	// Signed-in non-admin is forbidden.
	req = httptest.NewRequest(http.MethodGet, "/v1/admin/rooms", nil)
	req.AddCookie(signedCookie(t, secret, "alice"))
	rr = httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin list = %d, want 403", rr.Code)
	}

	// Anonymous is forbidden.
	req = httptest.NewRequest(http.MethodGet, "/v1/admin/rooms", nil)
	rr = httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("anon list = %d, want 403", rr.Code)
	}
}

func TestAdminRoomsAuthDisabled404(t *testing.T) {
	store := memory.NewStore()
	cfg := config.Config{Admins: []string{"root"}} // no GitHub/session → auth disabled
	s := NewServer(cfg, chat.NewService(store), store, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/rooms", nil)
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("auth-disabled list = %d, want 404", rr.Code)
	}
}

func TestAdminUsersEndpoint(t *testing.T) {
	secret := "secret-32-bytes-or-so-here-12345"
	s := newAdminServer(t, secret, "root")
	now := time.Now().UTC()

	if err := s.rooms.RecordUserActivity(t.Context(), models.UserActivity{
		Login:       "alice",
		Name:        "Alice",
		Email:       "alice@example.com",
		FirstSeenAt: now.Add(-48 * time.Hour),
		LastLoginAt: now.Add(-2 * time.Hour),
		LoginCount:  3,
	}, false); err != nil {
		t.Fatalf("seed user activity: %v", err)
	}
	owner := "alice"
	if err := s.rooms.CreateRoom(t.Context(), models.Room{
		ID:         "alice-room",
		OwnerLogin: &owner,
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("seed room: %v", err)
	}
	online := &client{
		roomID: "alice-room",
		send:   make(chan outgoing, 1),
		participant: models.Participant{
			ID:          "alice",
			RoomID:      "alice-room",
			Kind:        models.SenderKindUser,
			Label:       "Alice",
			ConnectedAt: now.Add(-10 * time.Minute),
			LastSeenAt:  now.Add(-1 * time.Minute),
		},
	}
	s.hub.register(online)
	defer s.hub.unregister(online)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil)
	req.AddCookie(signedCookie(t, secret, "root"))
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin users = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var report models.AdminUsersReport
	if err := json.NewDecoder(rr.Body).Decode(&report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.TotalUsers != 1 || report.OnlineUsers != 1 {
		t.Fatalf("totals = users %d online %d, want 1/1", report.TotalUsers, report.OnlineUsers)
	}
	if len(report.Trend) != 7 {
		t.Fatalf("trend len = %d, want 7", len(report.Trend))
	}
	if len(report.Users) != 1 {
		t.Fatalf("users len = %d, want 1", len(report.Users))
	}
	got := report.Users[0]
	if got.Login != "alice" || !got.Online || got.ConnectionCount != 1 || got.RoomsCreated != 1 {
		t.Fatalf("alice row = %+v, want online with one connection and one room", got)
	}
	if len(got.OnlineRoomIDs) != 1 || got.OnlineRoomIDs[0] != "alice-room" {
		t.Fatalf("online rooms = %#v, want alice-room", got.OnlineRoomIDs)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil)
	req.AddCookie(signedCookie(t, secret, "alice"))
	rr = httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin users = %d, want 403", rr.Code)
	}
}

func TestMeReportsAdmin(t *testing.T) {
	secret := "secret-32-bytes-or-so-here-12345"
	s := newAdminServer(t, secret, "root")

	get := func(login string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.AddCookie(signedCookie(t, secret, login))
		rr := httptest.NewRecorder()
		s.Routes().ServeHTTP(rr, req)
		var body map[string]any
		_ = json.NewDecoder(rr.Body).Decode(&body)
		return body
	}

	if body := get("root"); body["is_admin"] != true {
		t.Fatalf("root is_admin = %v, want true", body["is_admin"])
	}
	if body := get("alice"); body["is_admin"] != false {
		t.Fatalf("alice is_admin = %v, want false", body["is_admin"])
	}
}
