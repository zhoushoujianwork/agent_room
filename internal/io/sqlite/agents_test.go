package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-room/internal/models"
)

func TestAgentUpsertAndListByOwner(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.UpsertAgent(ctx, models.Agent{
		AgentID: "agent-1", OwnerLogin: "alice", Label: "Office Mac", Provider: "claude", LastSeenAt: now,
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	// Second agent for a different owner — must not leak into alice's list.
	if err := store.UpsertAgent(ctx, models.Agent{
		AgentID: "agent-2", OwnerLogin: "bob", Label: "Bob box", Provider: "claude", LastSeenAt: now,
	}); err != nil {
		t.Fatalf("UpsertAgent bob: %v", err)
	}

	got, err := store.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.OwnerLogin != "alice" || got.Label != "Office Mac" || got.Provider != "claude" {
		t.Fatalf("agent = %+v", got)
	}

	list, err := store.ListAgentsByOwner(ctx, "alice")
	if err != nil {
		t.Fatalf("ListAgentsByOwner: %v", err)
	}
	if len(list) != 1 || list[0].AgentID != "agent-1" {
		t.Fatalf("alice agents = %+v", list)
	}

	// Re-upsert with an empty label/provider must not wipe existing values.
	if err := store.UpsertAgent(ctx, models.Agent{
		AgentID: "agent-1", OwnerLogin: "alice", LastSeenAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ = store.GetAgent(ctx, "agent-1")
	if got.Label != "Office Mac" || got.Provider != "claude" {
		t.Fatalf("label/provider wiped on re-upsert: %+v", got)
	}
}

func TestAgentRevokeHidesFromList(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	_ = store.UpsertAgent(ctx, models.Agent{AgentID: "agent-1", OwnerLogin: "alice", LastSeenAt: time.Now().UTC()})
	if err := store.RevokeAgent(ctx, "agent-1"); err != nil {
		t.Fatalf("RevokeAgent: %v", err)
	}
	list, _ := store.ListAgentsByOwner(ctx, "alice")
	if len(list) != 0 {
		t.Fatalf("revoked agent still listed: %+v", list)
	}
	// Revoking an unknown agent is a NotFound.
	if err := store.RevokeAgent(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RevokeAgent unknown = %v, want ErrNotFound", err)
	}
	// A reconnect (upsert) re-binds, clearing revoked.
	_ = store.UpsertAgent(ctx, models.Agent{AgentID: "agent-1", OwnerLogin: "alice", LastSeenAt: time.Now().UTC()})
	list, _ = store.ListAgentsByOwner(ctx, "alice")
	if len(list) != 1 {
		t.Fatalf("re-bound agent not listed: %+v", list)
	}
}

func TestAgentTokenLifecycle(t *testing.T) {
	store := openTempStore(t)
	ctx := context.Background()

	const hash = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	if err := store.InsertAgentToken(ctx, models.AgentToken{
		TokenHash: hash, OwnerLogin: "alice", Note: "office",
	}); err != nil {
		t.Fatalf("InsertAgentToken: %v", err)
	}

	rec, err := store.LookupAgentToken(ctx, hash)
	if err != nil {
		t.Fatalf("LookupAgentToken: %v", err)
	}
	if rec.OwnerLogin != "alice" || rec.Revoked || rec.LastUsedAt != nil {
		t.Fatalf("token = %+v", rec)
	}
	if rec.HashPrefix != hash[:12] {
		t.Fatalf("HashPrefix = %q", rec.HashPrefix)
	}

	if err := store.TouchAgentTokenLastUsed(ctx, hash, time.Now().UTC()); err != nil {
		t.Fatalf("TouchAgentTokenLastUsed: %v", err)
	}
	rec, _ = store.LookupAgentToken(ctx, hash)
	if rec.LastUsedAt == nil {
		t.Fatalf("last_used_at not updated")
	}

	tokens, err := store.ListAgentTokensByOwner(ctx, "alice")
	if err != nil || len(tokens) != 1 {
		t.Fatalf("ListAgentTokensByOwner = %+v err=%v", tokens, err)
	}

	// Revoke by prefix, scoped to owner.
	n, err := store.RevokeAgentTokenByPrefix(ctx, "alice", hash[:12])
	if err != nil || n != 1 {
		t.Fatalf("RevokeAgentTokenByPrefix = %d err=%v", n, err)
	}
	rec, _ = store.LookupAgentToken(ctx, hash)
	if !rec.Revoked {
		t.Fatalf("token not revoked")
	}
	tokens, _ = store.ListAgentTokensByOwner(ctx, "alice")
	if len(tokens) != 0 {
		t.Fatalf("revoked token still listed: %+v", tokens)
	}
	// Another user cannot revoke alice's token.
	if n, _ := store.RevokeAgentTokenByPrefix(ctx, "bob", hash[:12]); n != 0 {
		t.Fatalf("cross-owner revoke matched %d, want 0", n)
	}
}
