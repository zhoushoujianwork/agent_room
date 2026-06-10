package summary

import (
	"fmt"
	"strings"

	"agent-room/internal/models"
)

const summarySystemPrompt = "You maintain a rolling summary of a multi-party chat room used by humans and AI agents collaborating on technical tasks. " +
	"Produce a concise, factual digest that a new agent joining the room could read to understand what has happened. " +
	"Capture: decisions made, tasks requested and their status, important facts/values established (but NEVER secrets, tokens, keys, or passwords — refer to them by name only), open questions, and who is doing what. " +
	"Hard length budget: keep the whole summary under roughly 1000 Chinese characters (or 600 English words). " +
	"This is a rolling summary — when updating, compress or drop older resolved details instead of letting it grow; never exceed the budget. " +
	"Write in the same language the room mostly uses. Output only the summary text, no preamble."

// buildSummaryPrompt combines the previous rolling summary (if any) with the
// recent raw messages into a single update prompt.
func buildSummaryPrompt(prev string, messages []models.ChatMessage) string {
	var b strings.Builder
	if strings.TrimSpace(prev) != "" {
		b.WriteString("Existing summary so far:\n")
		b.WriteString(strings.TrimSpace(prev))
		b.WriteString("\n\n")
		b.WriteString("New messages since then (update the summary to incorporate these; keep it coherent and deduplicated):\n")
	} else {
		b.WriteString("Room messages to summarize:\n")
	}
	for _, m := range messages {
		target := m.TargetID
		if target == "" {
			target = "room"
		}
		b.WriteString(fmt.Sprintf("[%s/%s -> %s] %s\n", m.SenderID, m.SenderKind, target, strings.TrimSpace(m.Content)))
	}
	b.WriteString("\nUpdated summary:")
	return b.String()
}
