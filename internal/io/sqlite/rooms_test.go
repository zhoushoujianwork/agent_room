package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-room/internal/models"
)

func TestSqliteMigrationIdempotent(t *testing.T) {
	// Open() already runs migrate() once. Running it again should be a
	// no-op, not error out — the contract calls this explicitly.
	store := openTempStore(t)
	if err := store.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("third migrate: %v", err)
	}
}

func TestRoomRoundTrip(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	login := "alice"
	title := "Daily standup"
	room := models.Room{
		ID:         "room-aaaa",
		OwnerLogin: &login,
		Title:      &title,
		Gated:      true,
		Ended:      false,
		CreatedAt:  time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := store.CreateRoom(ctx, room); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	got, err := store.GetRoom(ctx, room.ID)
	if err != nil {
		t.Fatalf("GetRoom: %v", err)
	}
	if got.OwnerLogin == nil || *got.OwnerLogin != login {
		t.Fatalf("owner = %v", got.OwnerLogin)
	}
	if got.Title == nil || *got.Title != title {
		t.Fatalf("title = %v", got.Title)
	}
	if !got.Gated || got.Ended {
		t.Fatalf("flags wrong: %+v", got)
	}

	// Update.
	newTitle := "Standup v2"
	got.Title = &newTitle
	got.Ended = true
	if err := store.UpdateRoom(ctx, *got); err != nil {
		t.Fatalf("UpdateRoom: %v", err)
	}
	again, err := store.GetRoom(ctx, room.ID)
	if err != nil {
		t.Fatalf("GetRoom after update: %v", err)
	}
	if again.Title == nil || *again.Title != newTitle || !again.Ended {
		t.Fatalf("update did not persist: %+v", again)
	}

	// Missing rooms return ErrNotFound.
	if _, err := store.GetRoom(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRoom missing err = %v, want ErrNotFound", err)
	}
}

func TestAccessRequestRoundTrip(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	if err := store.CreateRoom(ctx, models.Room{
		ID:        "room-aa",
		Gated:     true,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	login := "bob"
	req := models.AccessRequest{
		ID:              "req_aaa",
		RoomID:          "room-aa",
		RequesterLogin:  &login,
		RequesterAnonID: "anon-1",
		RequesterLabel:  "Bob",
		Via:             "GitHub · 房间链接",
		Status:          models.AccessRequestStatusPending,
		CreatedAt:       time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := store.CreateAccessRequest(ctx, req); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}

	got, err := store.GetAccessRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("GetAccessRequest: %v", err)
	}
	if got.RequesterLogin == nil || *got.RequesterLogin != login {
		t.Fatalf("requester_login = %v", got.RequesterLogin)
	}

	// Pending lookup.
	pending, err := store.FindPendingByRequester(ctx, "room-aa", login, "")
	if err != nil {
		t.Fatalf("FindPendingByRequester: %v", err)
	}
	if pending.ID != req.ID {
		t.Fatalf("pending mismatch")
	}

	// Approve.
	persist := models.AccessRequestPersistencePersist
	now := time.Now().UTC().Truncate(time.Microsecond)
	got.Status = models.AccessRequestStatusApproved
	got.Persistence = &persist
	got.ResolvedAt = &now
	if err := store.UpdateAccessRequest(ctx, *got); err != nil {
		t.Fatalf("UpdateAccessRequest: %v", err)
	}

	approved, err := store.FindApprovedByRequester(ctx, "room-aa", login, "")
	if err != nil {
		t.Fatalf("FindApprovedByRequester: %v", err)
	}
	if approved.Status != models.AccessRequestStatusApproved {
		t.Fatalf("approved status = %s", approved.Status)
	}
	if approved.Persistence == nil || *approved.Persistence != models.AccessRequestPersistencePersist {
		t.Fatalf("persistence wrong: %v", approved.Persistence)
	}

	// List.
	list, err := store.ListAccessRequests(ctx, "room-aa", 0)
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	// Cascade.
	if err := store.DeleteAccessRequestsByRoom(ctx, "room-aa"); err != nil {
		t.Fatalf("DeleteAccessRequestsByRoom: %v", err)
	}
	leftover, _ := store.ListAccessRequests(ctx, "room-aa", 0)
	if len(leftover) != 0 {
		t.Fatalf("cascade left %d requests", len(leftover))
	}
}
