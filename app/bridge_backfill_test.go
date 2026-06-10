package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-room/internal/models"
)

func TestFetchRoomHistoryFiltersTrace(t *testing.T) {
	msgs := []models.ChatMessage{
		{ID: "1", RoomID: "room1", Type: models.MessageTypeChat, Content: "hello"},
		{ID: "2", RoomID: "room1", Type: models.MessageTypeTrace, Content: "thinking"},
		{ID: "3", RoomID: "room1", Type: models.MessageTypePresence, Content: "agent joined"},
		{ID: "4", RoomID: "room1", Type: models.MessageTypeCommand, Content: "ls"},
		{ID: "5", RoomID: "room1", Type: models.MessageTypeCommandResult, Content: "done"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/v1/rooms/room1/messages" {
			t.Errorf("unexpected path %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(msgs)
	}))
	defer srv.Close()

	got, err := fetchRoomHistory(context.Background(), srv.URL, "room1", 100)
	if err != nil {
		t.Fatalf("fetchRoomHistory: %v", err)
	}
	// chat + presence + command + command_result kept; trace dropped.
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4 (trace dropped), got %#v", len(got), got)
	}
	for _, m := range got {
		if m.Type == models.MessageTypeTrace {
			t.Fatalf("trace leaked into history: %#v", m)
		}
	}
}

func TestFetchRoomHistoryNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := fetchRoomHistory(context.Background(), srv.URL, "room1", 50); err == nil {
		t.Fatal("expected error on non-200, got nil")
	}
}

func TestSummarizeHistory(t *testing.T) {
	empty := summarizeHistory(nil)
	if empty.Total != 0 || empty.ByType != "" || empty.OldestAge != "" {
		t.Fatalf("empty stats unexpected: %#v", empty)
	}

	now := time.Now()
	msgs := []models.ChatMessage{
		{Type: models.MessageTypeChat, CreatedAt: now.Add(-2 * time.Hour)},
		{Type: models.MessageTypeChat, CreatedAt: now.Add(-time.Hour)},
		{Type: models.MessageTypeCommand, CreatedAt: now},
		{Type: models.MessageTypePresence, CreatedAt: now},
	}
	stats := summarizeHistory(msgs)
	if stats.Total != 4 {
		t.Fatalf("total = %d want 4", stats.Total)
	}
	// chat must come before command/presence in the stable order, with counts.
	if stats.ByType != "chat=2 command=1 presence=1" {
		t.Fatalf("by_type = %q", stats.ByType)
	}
	if stats.OldestAge == "" {
		t.Fatal("oldest_age should be set when messages have timestamps")
	}
}
