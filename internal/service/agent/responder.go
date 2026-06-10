package agent

import (
	"context"
	"strings"
	"time"

	"agent-room/internal/models"
	"agent-room/pkg/id"
)

type Responder struct {
	agentID      string
	agentLabel   string
	capabilities string
	systemPrompt string
	contextDoc   string
	historyLimit int
	maxTurns     int
	relayHTTPURL string
	provider     models.AgentProvider
}

func NewResponder(agentID, agentLabel, capabilities, systemPrompt, contextDoc string, historyLimit int, maxTurns int, relayHTTPURL string, provider models.AgentProvider) *Responder {
	if historyLimit <= 0 {
		historyLimit = 30
	}
	if agentLabel == "" {
		agentLabel = agentID
	}
	if maxTurns <= 0 {
		maxTurns = 1
	}
	return &Responder{
		agentID:      agentID,
		agentLabel:   agentLabel,
		capabilities: capabilities,
		systemPrompt: systemPrompt,
		contextDoc:   contextDoc,
		historyLimit: historyLimit,
		maxTurns:     maxTurns,
		relayHTTPURL: relayHTTPURL,
		provider:     provider,
	}
}

func (r *Responder) ShouldReply(msg models.ChatMessage) bool {
	if msg.Type != "" && msg.Type != models.MessageTypeChat {
		return false
	}
	if strings.TrimSpace(msg.Content) == "" {
		return false
	}
	if msg.SenderID == r.agentID {
		return false
	}
	if !msg.IsAddressedTo(r.agentID) {
		return false
	}
	return msg.ReplyRequested && msg.TurnBudget > 0
}

func (r *Responder) Reply(ctx context.Context, msg models.ChatMessage, history []models.ChatMessage, roomSummary string, onEvent func(models.ProviderEvent), requestPermission func(context.Context, models.PermissionRequest) (models.PermissionDecision, error), delegateExec func(context.Context, models.DelegateExecRequest) (models.DelegateExecResult, error)) (models.ChatMessage, error) {
	resp, err := r.provider.Complete(ctx, models.ProviderRequest{
		RoomID:            msg.RoomID,
		AgentID:           r.agentID,
		AgentLabel:        r.agentLabel,
		Capabilities:      r.capabilities,
		Input:             msg,
		History:           last(history, r.historyLimit),
		Attachments:       resolveAttachments(msg, r.relayHTTPURL),
		RoomSummary:       roomSummary,
		ContextDoc:        r.contextDoc,
		SystemPrompt:      r.systemPrompt,
		MaxTurns:          r.maxTurns,
		RelayHTTPURL:      r.relayHTTPURL,
		OnEvent:           onEvent,
		RequestPermission: requestPermission,
		DelegateExec:      delegateExec,
	})
	if err != nil {
		return models.ChatMessage{}, err
	}

	turnBudget := msg.TurnBudget - 1
	return models.ChatMessage{
		ID:             id.New("msg"),
		RoomID:         msg.RoomID,
		Type:           models.MessageTypeChat,
		SenderID:       r.agentID,
		SenderKind:     models.SenderKindAgent,
		TargetID:       msg.SenderID,
		Content:        strings.TrimSpace(resp.Content),
		ReplyRequested: turnBudget > 0,
		TurnBudget:     turnBudget,
		CreatedAt:      time.Now().UTC(),
		Metadata: map[string]string{
			"provider":   r.provider.Name(),
			"session_id": resp.SessionID,
		},
	}, nil
}

// resolveAttachments 把入站消息 metadata 里的附件引用解析成可直接 GET 的
// 绝对 URL(指向 relay 的附件下载端点)。relay HTTP base 未知时返回 nil ——
// agent 反正拉不到相对路径。附件 URL 含 room id,授权边界与房间消息一致。
func resolveAttachments(msg models.ChatMessage, relayHTTPURL string) []models.ProviderAttachment {
	base := strings.TrimRight(strings.TrimSpace(relayHTTPURL), "/")
	if base == "" {
		return nil
	}
	refs := models.DecodeAttachmentList(msg.Metadata[models.MetadataKeyAttachments])
	if len(refs) == 0 {
		return nil
	}
	out := make([]models.ProviderAttachment, 0, len(refs))
	for _, ref := range refs {
		out = append(out, models.ProviderAttachment{
			URL:  base + "/v1/rooms/" + msg.RoomID + "/attachments/" + ref.ID,
			MIME: ref.MIME,
			Name: ref.Name,
		})
	}
	return out
}

func last(messages []models.ChatMessage, limit int) []models.ChatMessage {
	if limit <= 0 {
		return messages
	}
	// Presence messages drive participant + executor-peer awareness in the
	// prompt and are small and bounded, so they are always retained. The
	// limit applies only to the conversational tail (chat/command/etc), so a
	// long backfilled history can't push an executor's early "joined" notice
	// out of the window and silently disable delegation.
	convCount := 0
	for _, m := range messages {
		if m.Type != models.MessageTypePresence {
			convCount++
		}
	}
	if convCount <= limit {
		return messages
	}
	drop := convCount - limit // number of oldest conversational messages to skip
	out := make([]models.ChatMessage, 0, len(messages))
	for _, m := range messages {
		if m.Type != models.MessageTypePresence && drop > 0 {
			drop--
			continue
		}
		out = append(out, m)
	}
	return out
}
