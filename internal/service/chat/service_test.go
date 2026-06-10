package chat

import (
	"testing"

	"agent-room/internal/models"
)

func TestNormalizeSetsMentionTarget(t *testing.T) {
	msg := Normalize("demo", models.ChatMessage{
		Type:    models.MessageTypeChat,
		Content: "@alice please check this",
	})

	if msg.TargetID != "alice" {
		t.Fatalf("TargetID = %q, want alice", msg.TargetID)
	}
}

func TestNormalizeKeepsExplicitTarget(t *testing.T) {
	msg := Normalize("demo", models.ChatMessage{
		Type:     models.MessageTypeChat,
		TargetID: "bob",
		Content:  "@alice please check this",
	})

	if msg.TargetID != "bob" {
		t.Fatalf("TargetID = %q, want bob", msg.TargetID)
	}
}
