package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agent-room/internal/models"
)

// contextMessageTypes are the message kinds worth seeding into a bridge's
// local history on join. Trace (thinking/tool steps) is high-volume runtime
// noise and is dropped. Presence is kept — not as readable conversation, but
// because the prompt builder derives room participants and executor peers
// from presence messages. The prompt builder itself filters presence/trace
// out of the human-readable "Recent room history" section.
var contextMessageTypes = map[models.MessageType]bool{
	models.MessageTypeChat:          true,
	models.MessageTypeCommand:       true,
	models.MessageTypeCommandResult: true,
	models.MessageTypeSystem:        true,
	models.MessageTypePresence:      true,
}

// fetchRoomHistory pulls recent messages from the relay's REST API so a
// freshly (re)connected bridge starts with the room's real history instead
// of an empty in-memory store. Returns messages oldest-first (the relay
// already orders them that way). Trace noise is dropped; presence is kept
// for participant/executor awareness.
func fetchRoomHistory(ctx context.Context, httpBase, roomID string, limit int) ([]models.ChatMessage, error) {
	if httpBase == "" || roomID == "" {
		return nil, fmt.Errorf("relay http base and room id required")
	}
	if limit <= 0 {
		limit = 100
	}
	endpoint := fmt.Sprintf("%s/v1/rooms/%s/messages?limit=%d", httpBase, url.PathEscape(roomID), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("relay history GET %s: status %d", endpoint, resp.StatusCode)
	}
	var all []models.ChatMessage
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, fmt.Errorf("decode room history: %w", err)
	}
	out := make([]models.ChatMessage, 0, len(all))
	for _, msg := range all {
		if contextMessageTypes[msg.Type] {
			out = append(out, msg)
		}
	}
	return out, nil
}

// historyStats summarizes a message slice for structured logging so the
// bridge's logs show *what* context it loaded, not just a total count:
// per-type breakdown ("chat=18 command=4 ...") and the time span covered.
type historyStats struct {
	Total     int
	ByType    string
	OldestAge string
}

func summarizeHistory(messages []models.ChatMessage) historyStats {
	stats := historyStats{Total: len(messages)}
	if len(messages) == 0 {
		return stats
	}
	counts := map[models.MessageType]int{}
	var oldest time.Time
	for _, m := range messages {
		counts[m.Type]++
		if oldest.IsZero() || m.CreatedAt.Before(oldest) {
			if !m.CreatedAt.IsZero() {
				oldest = m.CreatedAt
			}
		}
	}
	// Stable, readable order for the common types.
	order := []models.MessageType{
		models.MessageTypeChat,
		models.MessageTypeCommand,
		models.MessageTypeCommandResult,
		models.MessageTypeSystem,
		models.MessageTypePresence,
	}
	parts := make([]string, 0, len(order))
	for _, t := range order {
		if n := counts[t]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", t, n))
		}
	}
	stats.ByType = strings.Join(parts, " ")
	if !oldest.IsZero() {
		stats.OldestAge = time.Since(oldest).Round(time.Second).String()
	}
	return stats
}
func fetchRoomSummary(ctx context.Context, httpBase, roomID string) string {
	if httpBase == "" || roomID == "" {
		return ""
	}
	endpoint := fmt.Sprintf("%s/v1/rooms/%s/summary", httpBase, url.PathEscape(roomID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var sum models.RoomSummary
	if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil {
		return ""
	}
	return sum.Summary
}
