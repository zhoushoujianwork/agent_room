// Package summary maintains a rolling, LLM-generated digest of each room's
// history. The relay calls Notify after persisting a message; the service
// decides (by message count or elapsed time) whether to regenerate, and does
// so in the background with one in-flight job per room. It is entirely
// opt-in: when no LLM client is configured the constructor returns nil and
// every call is a safe no-op.
package summary

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"agent-room/internal/io/llm"
	"agent-room/internal/models"
)

// Completer is the subset of an LLM client the summarizer needs.
type Completer interface {
	Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error)
}

// summaryMaxTokens caps the generated digest. Dense Chinese rooms burn
// roughly one token per character, so this leaves comfortable headroom above
// the ~1000-character budget the system prompt asks for.
const summaryMaxTokens = 2048

// Store is the persistence the summarizer needs: read recent messages, read
// and write the rolling summary.
type Store interface {
	models.MessageStore
	models.SummaryStore
}

type Service struct {
	store     Store
	llm       Completer
	logger    *slog.Logger
	every     int
	interval  time.Duration
	maxRecent int

	mu       sync.Mutex
	inFlight map[string]bool // rooms currently being summarized
	pending  map[string]int  // new-message counter since last summary, per room
	lastRun  map[string]time.Time
}

// New returns a summary service, or nil when llm is nil (opt-in off). A nil
// *Service is safe: all methods are no-ops on a nil receiver.
func New(store Store, llm Completer, every int, interval time.Duration, logger *slog.Logger) *Service {
	if llm == nil {
		return nil
	}
	if every <= 0 {
		every = 20
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Service{
		store:     store,
		llm:       llm,
		logger:    logger,
		every:     every,
		interval:  interval,
		maxRecent: 200,
		inFlight:  make(map[string]bool),
		pending:   make(map[string]int),
		lastRun:   make(map[string]time.Time),
	}
}

// Notify records that a new conversational message landed in a room and may
// trigger a background summary refresh. Safe to call on a nil service.
func (s *Service) Notify(roomID string, msgType models.MessageType) {
	if s == nil || roomID == "" {
		return
	}
	// Only conversational content is worth counting toward a refresh; trace
	// and presence are plumbing.
	switch msgType {
	case models.MessageTypeChat, models.MessageTypeCommand, models.MessageTypeCommandResult:
	default:
		return
	}

	s.mu.Lock()
	s.pending[roomID]++
	count := s.pending[roomID]
	last := s.lastRun[roomID]
	due := count >= s.every || (!last.IsZero() && time.Since(last) >= s.interval)
	if count == 1 && last.IsZero() {
		// First message in a fresh room: start the interval clock but don't
		// summarize a single line.
		s.lastRun[roomID] = time.Now()
	}
	shouldRun := due && !s.inFlight[roomID]
	if shouldRun {
		s.inFlight[roomID] = true
		s.pending[roomID] = 0
		s.lastRun[roomID] = time.Now()
	}
	s.mu.Unlock()

	if shouldRun {
		go s.run(roomID)
	}
}

func (s *Service) run(roomID string) {
	defer func() {
		s.mu.Lock()
		s.inFlight[roomID] = false
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := s.Refresh(ctx, roomID); err != nil {
		s.logger.Warn("room summary refresh failed", slog.String("room_id", roomID), slog.Any("error", err))
	}
}

// Refresh regenerates the summary for a room synchronously. Exposed for an
// explicit on-demand rebuild; Notify drives the usual background path.
func (s *Service) Refresh(ctx context.Context, roomID string) error {
	if s == nil {
		return nil
	}
	prev, err := s.store.GetSummary(ctx, roomID)
	if err != nil {
		return err
	}
	messages, err := s.store.List(ctx, roomID, s.maxRecent)
	if err != nil {
		return err
	}
	convo := filterConversational(messages)
	if len(convo) == 0 {
		return nil
	}

	prompt := buildSummaryPrompt(prev.Summary, convo)
	out, err := s.llm.Complete(ctx, summarySystemPrompt, prompt, summaryMaxTokens)
	if errors.Is(err, llm.ErrTruncated) {
		// The model hit max_tokens mid-sentence. Salvage by dropping the
		// dangling last line; a slightly shorter summary beats a visibly
		// cut-off one, and the prompt's length budget keeps this rare.
		s.logger.Warn("room summary truncated at max_tokens, trimming dangling line",
			slog.String("room_id", roomID))
		out = trimDanglingLine(out)
	} else if err != nil {
		return err
	}

	return s.store.UpsertSummary(ctx, models.RoomSummary{
		RoomID:     roomID,
		Summary:    strings.TrimSpace(out),
		CoveredSeq: prev.CoveredSeq + int64(len(convo)),
		UpdatedAt:  time.Now().UTC(),
	})
}

// trimDanglingLine drops the final line of a max_tokens-truncated output —
// it almost certainly stops mid-sentence. Single-line outputs are kept as-is.
func trimDanglingLine(s string) string {
	if i := strings.LastIndexByte(strings.TrimRight(s, "\n"), '\n'); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func filterConversational(messages []models.ChatMessage) []models.ChatMessage {
	out := make([]models.ChatMessage, 0, len(messages))
	for _, m := range messages {
		switch m.Type {
		case models.MessageTypeChat, models.MessageTypeCommand, models.MessageTypeCommandResult, models.MessageTypeSystem:
			if strings.TrimSpace(m.Content) != "" {
				out = append(out, m)
			}
		}
	}
	return out
}
