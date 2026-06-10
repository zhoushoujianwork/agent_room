package memory

import (
	"context"

	"agent-room/internal/models"
)

func (s *Store) PutAttachment(_ context.Context, att models.Attachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attachments[att.RoomID] == nil {
		s.attachments[att.RoomID] = make(map[string]models.Attachment)
	}
	// 防 caller 后续改切片:存自己的拷贝。
	stored := att
	stored.Bytes = append([]byte(nil), att.Bytes...)
	s.attachments[att.RoomID][att.ID] = stored
	return nil
}

func (s *Store) GetAttachment(_ context.Context, roomID, id string) (*models.Attachment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	att, ok := s.attachments[roomID][id]
	if !ok {
		return nil, ErrNotFound
	}
	c := att
	c.Bytes = append([]byte(nil), att.Bytes...)
	return &c, nil
}

func (s *Store) DeleteAttachmentsByRoom(_ context.Context, roomID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attachments, roomID)
	return nil
}

func (s *Store) AttachmentRoomUsage(_ context.Context, roomID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for _, att := range s.attachments[roomID] {
		total += att.Size
	}
	return total, nil
}
