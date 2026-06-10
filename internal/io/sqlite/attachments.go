package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"agent-room/internal/models"
)

func (s *Store) PutAttachment(ctx context.Context, att models.Attachment) error {
	if _, err := s.db.ExecContext(ctx, `
        INSERT INTO attachments (id, room_id, mime, size, bytes, created_at)
        VALUES (?, ?, ?, ?, ?, ?)`,
		att.ID,
		att.RoomID,
		att.MIME,
		att.Size,
		att.Bytes,
		att.CreatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("sqlite put attachment: %w", err)
	}
	return nil
}

func (s *Store) GetAttachment(ctx context.Context, roomID, id string) (*models.Attachment, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT id, room_id, mime, size, bytes, created_at
        FROM attachments WHERE room_id = ? AND id = ?`, roomID, id)
	var (
		att        models.Attachment
		createdRaw string
	)
	if err := row.Scan(&att.ID, &att.RoomID, &att.MIME, &att.Size, &att.Bytes, &createdRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("sqlite scan attachment: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return nil, fmt.Errorf("sqlite parse attachment created_at %q: %w", createdRaw, err)
	}
	att.CreatedAt = createdAt
	return &att, nil
}

func (s *Store) DeleteAttachmentsByRoom(ctx context.Context, roomID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM attachments WHERE room_id = ?`, roomID); err != nil {
		return fmt.Errorf("sqlite delete attachments: %w", err)
	}
	return nil
}

func (s *Store) AttachmentRoomUsage(ctx context.Context, roomID string) (int64, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT COALESCE(SUM(size), 0) FROM attachments WHERE room_id = ?`, roomID)
	var total int64
	if err := row.Scan(&total); err != nil {
		return 0, fmt.Errorf("sqlite attachment usage: %w", err)
	}
	return total, nil
}
