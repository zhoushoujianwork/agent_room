package sqlite

import (
	"context"
	"testing"
	"time"

	"agent-room/internal/models"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSummaryRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Missing summary returns zero value, not error.
	got, err := st.GetSummary(ctx, "room1")
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if got.RoomID != "room1" || got.Summary != "" || got.CoveredSeq != 0 {
		t.Fatalf("empty summary unexpected: %#v", got)
	}

	want := models.RoomSummary{RoomID: "room1", Summary: "聊了出口IP和exec_token", CoveredSeq: 12, UpdatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := st.UpsertSummary(ctx, want); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err = st.GetSummary(ctx, "room1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Summary != want.Summary || got.CoveredSeq != want.CoveredSeq {
		t.Fatalf("got %#v want %#v", got, want)
	}

	// Upsert again replaces.
	want.Summary = "更新后的摘要"
	want.CoveredSeq = 30
	if err := st.UpsertSummary(ctx, want); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	got, _ = st.GetSummary(ctx, "room1")
	if got.Summary != "更新后的摘要" || got.CoveredSeq != 30 {
		t.Fatalf("replace failed: %#v", got)
	}
}

func TestSearchFilters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seed := []models.ChatMessage{
		{ID: "m1", RoomID: "r", Type: models.MessageTypeChat, Content: "看下出口IP", CreatedAt: time.Now()},
		{ID: "m2", RoomID: "r", Type: models.MessageTypeTrace, Content: "thinking about IP", CreatedAt: time.Now()},
		{ID: "m3", RoomID: "r", Type: models.MessageTypeChat, Content: "exec_token 是多少", CreatedAt: time.Now()},
		{ID: "m4", RoomID: "r", Type: models.MessageTypeCommand, Content: "curl ifconfig.me", CreatedAt: time.Now()},
	}
	for _, m := range seed {
		if err := st.Append(ctx, m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// Substring match across all types.
	got, err := st.Search(ctx, "r", models.MessageQuery{Q: "ip"})
	if err != nil {
		t.Fatalf("search q: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("q=ip len=%d want 2 (%#v)", len(got), contentsOf(got))
	}

	// Type filter excludes trace.
	got, _ = st.Search(ctx, "r", models.MessageQuery{Q: "ip", Types: []models.MessageType{models.MessageTypeChat}})
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("type filter unexpected: %#v", contentsOf(got))
	}

	// LIKE wildcards in query are matched literally.
	got, _ = st.Search(ctx, "r", models.MessageQuery{Q: "%"})
	if len(got) != 0 {
		t.Fatalf("literal %% should match nothing, got %d", len(got))
	}

	// BeforeID pages backwards: only rows older than the cursor, oldest-first.
	got, _ = st.Search(ctx, "r", models.MessageQuery{BeforeID: "m3"})
	if len(got) != 2 || got[0].ID != "m1" || got[1].ID != "m2" {
		t.Fatalf("beforeID unexpected: %#v", contentsOf(got))
	}
	// Limit keeps the newest page of the older rows.
	got, _ = st.Search(ctx, "r", models.MessageQuery{BeforeID: "m3", Limit: 1})
	if len(got) != 1 || got[0].ID != "m2" {
		t.Fatalf("beforeID+limit unexpected: %#v", contentsOf(got))
	}
	// Unknown cursor → no rows.
	got, _ = st.Search(ctx, "r", models.MessageQuery{BeforeID: "nope"})
	if len(got) != 0 {
		t.Fatalf("unknown beforeID should be empty, got %d", len(got))
	}
}

func contentsOf(msgs []models.ChatMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}
