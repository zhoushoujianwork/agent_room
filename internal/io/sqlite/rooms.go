package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"agent-room/internal/models"
)

// ErrNotFound signals that the requested room or access request does
// not exist. Callers translate this to 404.
var ErrNotFound = errors.New("not found")

func (s *Store) GetRoom(ctx context.Context, roomID string) (*models.Room, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT id, owner_login, title, gated, ended, created_at
        FROM rooms WHERE id = ?`, roomID)
	return scanRoom(row)
}

func (s *Store) CreateRoom(ctx context.Context, room models.Room) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO rooms (id, owner_login, title, gated, ended, created_at)
        VALUES (?, ?, ?, ?, ?, ?)`,
		room.ID,
		nullableString(room.OwnerLogin),
		nullableString(room.Title),
		boolToInt(room.Gated),
		boolToInt(room.Ended),
		room.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("sqlite create room: %w", err)
	}
	return nil
}

func (s *Store) UpdateRoom(ctx context.Context, room models.Room) error {
	res, err := s.db.ExecContext(ctx, `
        UPDATE rooms
        SET owner_login = ?, title = ?, gated = ?, ended = ?
        WHERE id = ?`,
		nullableString(room.OwnerLogin),
		nullableString(room.Title),
		boolToInt(room.Gated),
		boolToInt(room.Ended),
		room.ID,
	)
	if err != nil {
		return fmt.Errorf("sqlite update room: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListRooms(ctx context.Context, limit, offset int) ([]models.Room, error) {
	query := `
        SELECT id, owner_login, title, gated, ended, created_at
        FROM rooms
        ORDER BY created_at DESC`
	var args []any
	if limit > 0 || offset > 0 {
		// SQLite requires LIMIT before OFFSET; -1 means "no cap".
		if limit <= 0 {
			limit = -1
		}
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite list rooms: %w", err)
	}
	defer rows.Close()

	out := make([]models.Room, 0)
	for rows.Next() {
		room, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *room)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite list rooms rows: %w", err)
	}
	return out, nil
}

func (s *Store) DeleteRoom(ctx context.Context, roomID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rooms WHERE id = ?`, roomID); err != nil {
		return fmt.Errorf("sqlite delete room: %w", err)
	}
	return nil
}

func (s *Store) DeleteMessagesByRoom(ctx context.Context, roomID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE room_id = ?`, roomID); err != nil {
		return fmt.Errorf("sqlite delete messages: %w", err)
	}
	return nil
}

func (s *Store) DeleteAccessRequestsByRoom(ctx context.Context, roomID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM access_requests WHERE room_id = ?`, roomID); err != nil {
		return fmt.Errorf("sqlite delete access_requests: %w", err)
	}
	return nil
}

func (s *Store) CreateAccessRequest(ctx context.Context, req models.AccessRequest) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO access_requests (
            id, room_id, requester_login, requester_anonid, requester_label,
            via, location, status, persistence, created_at, resolved_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID,
		req.RoomID,
		nullableString(req.RequesterLogin),
		nullableNonEmpty(req.RequesterAnonID),
		req.RequesterLabel,
		req.Via,
		nullableString(req.Location),
		string(req.Status),
		nullablePersistence(req.Persistence),
		req.CreatedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(req.ResolvedAt),
	)
	if err != nil {
		return fmt.Errorf("sqlite create access_request: %w", err)
	}
	return nil
}

func (s *Store) GetAccessRequest(ctx context.Context, requestID string) (*models.AccessRequest, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT id, room_id, requester_login, requester_anonid, requester_label,
               via, location, status, persistence, created_at, resolved_at
        FROM access_requests WHERE id = ?`, requestID)
	return scanAccessRequest(row)
}

func (s *Store) UpdateAccessRequest(ctx context.Context, req models.AccessRequest) error {
	res, err := s.db.ExecContext(ctx, `
        UPDATE access_requests
        SET status = ?, persistence = ?, resolved_at = ?
        WHERE id = ?`,
		string(req.Status),
		nullablePersistence(req.Persistence),
		nullableTime(req.ResolvedAt),
		req.ID,
	)
	if err != nil {
		return fmt.Errorf("sqlite update access_request: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListAccessRequests(ctx context.Context, roomID string, limit int) ([]models.AccessRequest, error) {
	query := `
        SELECT id, room_id, requester_login, requester_anonid, requester_label,
               via, location, status, persistence, created_at, resolved_at
        FROM access_requests
        WHERE room_id = ?
        ORDER BY created_at DESC`
	args := []any{roomID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite list access_requests: %w", err)
	}
	defer rows.Close()

	out := make([]models.AccessRequest, 0)
	for rows.Next() {
		req, err := scanAccessRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite list access_requests rows: %w", err)
	}
	return out, nil
}

func (s *Store) FindPendingByRequester(ctx context.Context, roomID, login, anonID string) (*models.AccessRequest, error) {
	return s.findRequester(ctx, roomID, login, anonID, string(models.AccessRequestStatusPending))
}

func (s *Store) FindApprovedByRequester(ctx context.Context, roomID, login, anonID string) (*models.AccessRequest, error) {
	return s.findRequester(ctx, roomID, login, anonID, string(models.AccessRequestStatusApproved))
}

func (s *Store) findRequester(ctx context.Context, roomID, login, anonID, status string) (*models.AccessRequest, error) {
	// Match on either login or anon id, whichever is non-empty. Both
	// being empty means we have no identity to match — return NotFound.
	if login == "" && anonID == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
        SELECT id, room_id, requester_login, requester_anonid, requester_label,
               via, location, status, persistence, created_at, resolved_at
        FROM access_requests
        WHERE room_id = ?
          AND status = ?
          AND (
                (? <> '' AND requester_login = ?)
             OR (? <> '' AND requester_anonid = ?)
          )
        ORDER BY created_at DESC
        LIMIT 1`,
		roomID, status,
		login, login,
		anonID, anonID,
	)
	return scanAccessRequest(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRoom(s scanner) (*models.Room, error) {
	var (
		id         string
		owner      sql.NullString
		title      sql.NullString
		gated      int
		ended      int
		createdRaw string
	)
	if err := s.Scan(&id, &owner, &title, &gated, &ended, &createdRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("sqlite scan room: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return nil, fmt.Errorf("sqlite parse room created_at %q: %w", createdRaw, err)
	}
	room := &models.Room{
		ID:        id,
		Gated:     gated != 0,
		Ended:     ended != 0,
		CreatedAt: createdAt,
	}
	if owner.Valid {
		v := owner.String
		room.OwnerLogin = &v
	}
	if title.Valid {
		v := title.String
		room.Title = &v
	}
	return room, nil
}

func scanAccessRequest(s scanner) (*models.AccessRequest, error) {
	var (
		id         string
		roomID     string
		login      sql.NullString
		anonID     sql.NullString
		label      string
		via        string
		location   sql.NullString
		status     string
		persist    sql.NullString
		createdRaw string
		resolvedAt sql.NullString
	)
	if err := s.Scan(&id, &roomID, &login, &anonID, &label, &via, &location, &status, &persist, &createdRaw, &resolvedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("sqlite scan access_request: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return nil, fmt.Errorf("sqlite parse access_request created_at %q: %w", createdRaw, err)
	}
	req := &models.AccessRequest{
		ID:             id,
		RoomID:         roomID,
		RequesterLabel: label,
		Via:            via,
		Status:         models.AccessRequestStatus(status),
		CreatedAt:      createdAt,
	}
	if login.Valid {
		v := login.String
		req.RequesterLogin = &v
	}
	if anonID.Valid {
		req.RequesterAnonID = anonID.String
	}
	if location.Valid {
		v := location.String
		req.Location = &v
	}
	if persist.Valid {
		p := models.AccessRequestPersistence(persist.String)
		req.Persistence = &p
	}
	if resolvedAt.Valid {
		ts, err := time.Parse(time.RFC3339Nano, resolvedAt.String)
		if err != nil {
			return nil, fmt.Errorf("sqlite parse access_request resolved_at %q: %w", resolvedAt.String, err)
		}
		req.ResolvedAt = &ts
	}
	return req, nil
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func nullableNonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullablePersistence(p *models.AccessRequestPersistence) any {
	if p == nil {
		return nil
	}
	return string(*p)
}
