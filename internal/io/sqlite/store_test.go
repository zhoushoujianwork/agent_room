package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"agent-room/internal/models"
)

func TestStoreAppendAndListRoundTrip(t *testing.T) {
	store := openTempStore(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	msg := models.ChatMessage{
		ID:             "msg_1",
		RoomID:         "demo",
		Type:           models.MessageTypeChat,
		SenderID:       "alice",
		SenderKind:     models.SenderKindUser,
		TargetID:       "bob",
		Content:        "hello",
		ReplyRequested: true,
		TurnBudget:     2,
		CreatedAt:      now,
		Metadata:       map[string]string{"provider": "claude", "label": "Alice"},
	}
	if err := store.Append(context.Background(), msg); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := store.List(context.Background(), "demo", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ID != msg.ID || got[0].Content != msg.Content {
		t.Errorf("round trip mismatch: got %#v want %#v", got[0], msg)
	}
	if !got[0].ReplyRequested {
		t.Errorf("ReplyRequested lost")
	}
	if got[0].TurnBudget != 2 {
		t.Errorf("TurnBudget = %d, want 2", got[0].TurnBudget)
	}
	if !got[0].CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %s, want %s", got[0].CreatedAt, now)
	}
	if got[0].Metadata["provider"] != "claude" {
		t.Errorf("metadata lost: %#v", got[0].Metadata)
	}
}

func TestStoreListReturnsInsertionOrder(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	for i, id := range []string{"a", "b", "c", "d"} {
		if err := store.Append(ctx, models.ChatMessage{
			ID:        "msg_" + id,
			RoomID:    "demo",
			Type:      models.MessageTypeChat,
			Content:   id,
			CreatedAt: time.Date(2026, 5, 21, 12, 0, i, 0, time.UTC),
		}); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}

	got, err := store.List(ctx, "demo", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	want := []string{"a", "b", "c", "d"}
	for i, msg := range got {
		if msg.Content != want[i] {
			t.Errorf("got[%d].Content = %q, want %q", i, msg.Content, want[i])
		}
	}
}

func TestStoreListLimitReturnsTail(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := store.Append(ctx, models.ChatMessage{
			ID:        "msg_" + id,
			RoomID:    "demo",
			Content:   id,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}

	got, err := store.List(ctx, "demo", 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"d", "e"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, msg := range got {
		if msg.Content != want[i] {
			t.Errorf("got[%d].Content = %q, want %q", i, msg.Content, want[i])
		}
	}
}

func TestStoreListIsolatesByRoom(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	if err := store.Append(ctx, models.ChatMessage{ID: "msg_1", RoomID: "alpha", Content: "in-alpha", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Append alpha: %v", err)
	}
	if err := store.Append(ctx, models.ChatMessage{ID: "msg_2", RoomID: "beta", Content: "in-beta", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Append beta: %v", err)
	}

	alpha, err := store.List(ctx, "alpha", 0)
	if err != nil {
		t.Fatalf("List alpha: %v", err)
	}
	if len(alpha) != 1 || alpha[0].Content != "in-alpha" {
		t.Errorf("alpha = %#v, want one in-alpha", alpha)
	}
	beta, err := store.List(ctx, "beta", 0)
	if err != nil {
		t.Fatalf("List beta: %v", err)
	}
	if len(beta) != 1 || beta[0].Content != "in-beta" {
		t.Errorf("beta = %#v, want one in-beta", beta)
	}
}

func openTempStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
