package memory

import (
	"context"
	"testing"
	"time"

	"agent-room/internal/models"
)

func TestStoreListReturnsLatestMessages(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	for _, content := range []string{"one", "two", "three"} {
		if err := store.Append(ctx, models.ChatMessage{RoomID: "demo", Content: content, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, err := store.List(ctx, "demo", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Content != "two" || got[1].Content != "three" {
		t.Fatalf("got contents %#v", got)
	}
}

func TestStoreAppendDedupesByID(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	msg := models.ChatMessage{ID: "msg-1", RoomID: "demo", Content: "hello", CreatedAt: time.Now()}
	for i := 0; i < 3; i++ {
		if err := store.Append(ctx, msg); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	got, err := store.List(ctx, "demo", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (dedup by id)", len(got))
	}

	// Empty-ID messages are not deduped (legacy/test path).
	for i := 0; i < 2; i++ {
		if err := store.Append(ctx, models.ChatMessage{RoomID: "demo", Content: "anon", CreatedAt: time.Now()}); err != nil {
			t.Fatalf("append anon %d: %v", i, err)
		}
	}
	got, _ = store.List(ctx, "demo", 0)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (1 deduped + 2 anon)", len(got))
	}
}
