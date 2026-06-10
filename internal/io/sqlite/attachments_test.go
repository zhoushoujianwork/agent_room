package sqlite

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"agent-room/internal/models"
)

func TestAttachmentRoundTrip(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	att := models.Attachment{
		ID:        "att-1",
		RoomID:    "room-a",
		MIME:      "image/png",
		Size:      4,
		Bytes:     []byte{0x89, 'P', 'N', 'G'},
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := store.PutAttachment(ctx, att); err != nil {
		t.Fatalf("PutAttachment: %v", err)
	}

	got, err := store.GetAttachment(ctx, "room-a", "att-1")
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if got.MIME != att.MIME || got.Size != att.Size || !bytes.Equal(got.Bytes, att.Bytes) {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	// 错房间取不到 —— 房间 URL 即访问边界。
	if _, err := store.GetAttachment(ctx, "room-b", "att-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-room get: want ErrNotFound, got %v", err)
	}
}

func TestAttachmentRoomUsageAndDelete(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	for _, att := range []models.Attachment{
		{ID: "a1", RoomID: "room-a", MIME: "image/png", Size: 3, Bytes: []byte("aaa"), CreatedAt: time.Now().UTC()},
		{ID: "a2", RoomID: "room-a", MIME: "image/jpeg", Size: 5, Bytes: []byte("bbbbb"), CreatedAt: time.Now().UTC()},
		{ID: "b1", RoomID: "room-b", MIME: "image/png", Size: 7, Bytes: []byte("ccccccc"), CreatedAt: time.Now().UTC()},
	} {
		if err := store.PutAttachment(ctx, att); err != nil {
			t.Fatalf("PutAttachment %s: %v", att.ID, err)
		}
	}

	usage, err := store.AttachmentRoomUsage(ctx, "room-a")
	if err != nil {
		t.Fatalf("AttachmentRoomUsage: %v", err)
	}
	if usage != 8 {
		t.Fatalf("usage = %d, want 8", usage)
	}
	if usage, _ := store.AttachmentRoomUsage(ctx, "room-empty"); usage != 0 {
		t.Fatalf("empty room usage = %d, want 0", usage)
	}

	if err := store.DeleteAttachmentsByRoom(ctx, "room-a"); err != nil {
		t.Fatalf("DeleteAttachmentsByRoom: %v", err)
	}
	if _, err := store.GetAttachment(ctx, "room-a", "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	// 别的房间不受影响。
	if _, err := store.GetAttachment(ctx, "room-b", "b1"); err != nil {
		t.Fatalf("room-b attachment gone: %v", err)
	}
}
