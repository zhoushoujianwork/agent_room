package memory

import (
	"context"
	"testing"
	"time"

	"agent-room/internal/models"
)

func TestSummaryRoundTrip(t *testing.T) {
	st := NewStore()
	ctx := context.Background()

	got, err := st.GetSummary(ctx, "room1")
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if got.RoomID != "room1" || got.Summary != "" {
		t.Fatalf("empty summary unexpected: %#v", got)
	}

	want := models.RoomSummary{RoomID: "room1", Summary: "digest", CoveredSeq: 5, UpdatedAt: time.Now().UTC()}
	if err := st.UpsertSummary(ctx, want); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = st.GetSummary(ctx, "room1")
	if got.Summary != "digest" || got.CoveredSeq != 5 {
		t.Fatalf("got %#v", got)
	}
}

func TestSearchFiltersMemory(t *testing.T) {
	st := NewStore()
	ctx := context.Background()
	seed := []models.ChatMessage{
		{ID: "m1", RoomID: "r", Type: models.MessageTypeChat, Content: "看下出口IP"},
		{ID: "m2", RoomID: "r", Type: models.MessageTypeTrace, Content: "thinking about IP"},
		{ID: "m3", RoomID: "r", Type: models.MessageTypeChat, Content: "exec_token 是多少"},
	}
	for _, m := range seed {
		if err := st.Append(ctx, m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, err := st.Search(ctx, "r", models.MessageQuery{Q: "ip"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("q=ip len=%d want 2", len(got))
	}

	got, _ = st.Search(ctx, "r", models.MessageQuery{Q: "ip", Types: []models.MessageType{models.MessageTypeChat}})
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("type filter unexpected: %#v", got)
	}

	// SinceSeq excludes earlier messages.
	got, _ = st.Search(ctx, "r", models.MessageQuery{SinceSeq: 2})
	if len(got) != 1 || got[0].ID != "m3" {
		t.Fatalf("sinceSeq unexpected: %#v", got)
	}

	// BeforeID pages backwards: only messages older than the cursor.
	got, _ = st.Search(ctx, "r", models.MessageQuery{BeforeID: "m3"})
	if len(got) != 2 || got[0].ID != "m1" || got[1].ID != "m2" {
		t.Fatalf("beforeID unexpected: %#v", got)
	}
	got, _ = st.Search(ctx, "r", models.MessageQuery{BeforeID: "m3", Limit: 1})
	if len(got) != 1 || got[0].ID != "m2" {
		t.Fatalf("beforeID+limit should return newest-of-older first: %#v", got)
	}
	// An unknown cursor yields nothing (points at history we don't hold).
	got, _ = st.Search(ctx, "r", models.MessageQuery{BeforeID: "nope"})
	if len(got) != 0 {
		t.Fatalf("unknown beforeID should be empty: %#v", got)
	}
}
