package chat

import (
	"context"
	"regexp"
	"strings"
	"time"

	"agent-room/internal/models"
	"agent-room/pkg/id"
)

var mentionPattern = regexp.MustCompile(`(?:^|\s)@([a-zA-Z0-9_-]{1,64})(?:\s|$|[,:;.!?])`)

type Service struct {
	store models.MessageStore
}

func NewService(store models.MessageStore) *Service {
	return &Service{store: store}
}

func (s *Service) Add(ctx context.Context, roomID string, msg models.ChatMessage) (models.ChatMessage, error) {
	msg = Normalize(roomID, msg)
	if err := s.store.Append(ctx, msg); err != nil {
		return models.ChatMessage{}, err
	}
	return msg, nil
}

func (s *Service) List(ctx context.Context, roomID string, limit int) ([]models.ChatMessage, error) {
	return s.store.List(ctx, roomID, limit)
}

// Search filters a room's history by content/type/sequence when the
// underlying store supports it (models.MessageSearcher). Stores that do not
// implement search fall back to a plain List, ignoring the filters, so the
// caller always gets a usable result.
func (s *Service) Search(ctx context.Context, roomID string, q models.MessageQuery) ([]models.ChatMessage, error) {
	if searcher, ok := s.store.(models.MessageSearcher); ok {
		return searcher.Search(ctx, roomID, q)
	}
	return s.store.List(ctx, roomID, q.Limit)
}

func Normalize(roomID string, msg models.ChatMessage) models.ChatMessage {
	if msg.ID == "" {
		msg.ID = id.New("msg")
	}
	if msg.RoomID == "" {
		msg.RoomID = roomID
	}
	if msg.Type == "" {
		msg.Type = models.MessageTypeChat
	}
	if msg.SenderKind == "" {
		msg.SenderKind = models.SenderKindUser
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	msg.Content = strings.TrimSpace(msg.Content)
	if msg.TargetID == "" && msg.Type == models.MessageTypeChat {
		msg.TargetID = mentionTarget(msg.Content)
	}
	return msg
}

func mentionTarget(content string) string {
	match := mentionPattern.FindStringSubmatch(content)
	if len(match) < 2 {
		return ""
	}
	target := strings.TrimSpace(match[1])
	if strings.EqualFold(target, "all") || strings.EqualFold(target, "room") {
		return ""
	}
	return target
}
