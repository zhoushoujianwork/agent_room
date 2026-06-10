package agent

import (
	"context"
	"testing"

	"agent-room/internal/models"
)

type fakeProvider struct{}

func (fakeProvider) Name() string { return "fake" }

func (fakeProvider) Complete(_ context.Context, _ models.ProviderRequest) (models.ProviderResponse, error) {
	return models.ProviderResponse{Content: "hello from agent"}, nil
}

func TestResponderShouldReplyOnlyWhenAddressedAndBudgeted(t *testing.T) {
	responder := NewResponder("alice", "", "", "", "", 10, 1, "", fakeProvider{})

	tests := []struct {
		name string
		msg  models.ChatMessage
		want bool
	}{
		{
			name: "reply requested",
			msg:  models.ChatMessage{Type: models.MessageTypeChat, SenderID: "bob", Content: "hi", ReplyRequested: true, TurnBudget: 1},
			want: true,
		},
		{
			name: "self message",
			msg:  models.ChatMessage{Type: models.MessageTypeChat, SenderID: "alice", Content: "hi", ReplyRequested: true, TurnBudget: 1},
			want: false,
		},
		{
			name: "wrong target",
			msg:  models.ChatMessage{Type: models.MessageTypeChat, SenderID: "bob", TargetID: "carol", Content: "hi", ReplyRequested: true, TurnBudget: 1},
			want: false,
		},
		{
			name: "budget exhausted",
			msg:  models.ChatMessage{Type: models.MessageTypeChat, SenderID: "bob", Content: "hi", ReplyRequested: true, TurnBudget: 0},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responder.ShouldReply(tt.msg); got != tt.want {
				t.Fatalf("ShouldReply = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResponderReplyDecrementsTurnBudget(t *testing.T) {
	responder := NewResponder("alice", "", "", "", "", 10, 1, "", fakeProvider{})
	reply, err := responder.Reply(context.Background(), models.ChatMessage{
		RoomID:     "demo",
		SenderID:   "bob",
		Content:    "hi",
		TurnBudget: 2,
	}, nil, "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if reply.SenderID != "alice" {
		t.Fatalf("SenderID = %q", reply.SenderID)
	}
	if reply.TargetID != "bob" {
		t.Fatalf("TargetID = %q", reply.TargetID)
	}
	if reply.TurnBudget != 1 || !reply.ReplyRequested {
		t.Fatalf("turn budget = %d reply requested = %v", reply.TurnBudget, reply.ReplyRequested)
	}
}

func TestResolveAttachments(t *testing.T) {
	msg := models.ChatMessage{
		RoomID: "room1",
		Metadata: map[string]string{
			models.MetadataKeyAttachments: `[{"id":"a1","mime":"image/png","name":"shot.png"},{"id":"a2","mime":"image/jpeg"}]`,
		},
	}

	atts := resolveAttachments(msg, "https://relay.example/")
	if len(atts) != 2 {
		t.Fatalf("got %d attachments, want 2", len(atts))
	}
	if atts[0].URL != "https://relay.example/v1/rooms/room1/attachments/a1" {
		t.Fatalf("url = %q", atts[0].URL)
	}
	if atts[0].MIME != "image/png" || atts[0].Name != "shot.png" {
		t.Fatalf("ref fields lost: %+v", atts[0])
	}

	// relay HTTP base 未知时 agent 拉不到字节, 返回 nil。
	if got := resolveAttachments(msg, ""); got != nil {
		t.Fatalf("want nil without relay base, got %+v", got)
	}
	// 无附件 metadata。
	if got := resolveAttachments(models.ChatMessage{RoomID: "room1"}, "https://relay.example"); got != nil {
		t.Fatalf("want nil without metadata, got %+v", got)
	}
	// 坏 JSON 容忍。
	bad := models.ChatMessage{RoomID: "room1", Metadata: map[string]string{models.MetadataKeyAttachments: "{not json"}}
	if got := resolveAttachments(bad, "https://relay.example"); got != nil {
		t.Fatalf("want nil on bad json, got %+v", got)
	}
}
