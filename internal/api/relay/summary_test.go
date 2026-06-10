package relay

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-room/internal/config"
	"agent-room/internal/io/memory"
	"agent-room/internal/models"
	"agent-room/internal/service/chat"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestGetSummaryEmptyWhenUnconfigured(t *testing.T) {
	store := memory.NewStore()
	s := NewServer(config.Config{}, chat.NewService(store), store, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/rooms/r1/summary", nil)
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got models.RoomSummary
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Summary != "" {
		t.Fatalf("summary = %q, want empty", got.Summary)
	}
}

func TestGetSummaryReturnsStored(t *testing.T) {
	store := memory.NewStore()
	_ = store.UpsertSummary(context.Background(), models.RoomSummary{RoomID: "r1", Summary: "digest here", CoveredSeq: 7})
	s := NewServer(config.Config{}, chat.NewService(store), store, discardLogger())
	s.WithSummary(store, nil) // store wired, summarizer off

	req := httptest.NewRequest(http.MethodGet, "/v1/rooms/r1/summary", nil)
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)

	var got models.RoomSummary
	_ = json.NewDecoder(rr.Body).Decode(&got)
	if got.Summary != "digest here" || got.CoveredSeq != 7 {
		t.Fatalf("got %#v", got)
	}
}

func TestListMessagesSearchFilters(t *testing.T) {
	store := memory.NewStore()
	svc := chat.NewService(store)
	ctx := context.Background()
	for _, m := range []models.ChatMessage{
		{ID: "a", RoomID: "r1", Type: models.MessageTypeChat, Content: "看下出口IP"},
		{ID: "b", RoomID: "r1", Type: models.MessageTypeTrace, Content: "thinking IP"},
		{ID: "c", RoomID: "r1", Type: models.MessageTypeChat, Content: "放通8080"},
	} {
		if _, err := svc.Add(ctx, "r1", m); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	s := NewServer(config.Config{}, svc, store, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/rooms/r1/messages?q=IP&type=chat", nil)
	rr := httptest.NewRecorder()
	s.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var got []models.ChatMessage
	_ = json.NewDecoder(rr.Body).Decode(&got)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("q=IP&type=chat got %d messages: %#v", len(got), got)
	}
}
