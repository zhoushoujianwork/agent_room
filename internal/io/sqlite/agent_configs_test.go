package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"agent-room/internal/models"
)

func TestAgentConfigCRUD(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// The FK requires the agent row to exist first.
	if err := store.UpsertAgent(ctx, models.Agent{AgentID: "a1", OwnerLogin: "alice"}); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}

	// Missing config → ErrNotFound.
	if _, err := store.GetAgentConfig(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing config err = %v, want ErrNotFound", err)
	}

	// Insert then read back.
	if err := store.UpsertAgentConfig(ctx, models.AgentConfig{
		AgentID: "a1", Model: "m1", APIBaseURL: "https://x/v1", APIKeyCipher: "cipher==", UpdatedBy: "alice",
	}); err != nil {
		t.Fatalf("upsert config: %v", err)
	}
	got, err := store.GetAgentConfig(ctx, "a1")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if got.Model != "m1" || got.APIBaseURL != "https://x/v1" || got.APIKeyCipher != "cipher==" || got.UpdatedBy != "alice" {
		t.Fatalf("config mismatch: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("updated_at not set")
	}

	// Upsert replaces (clear the key).
	if err := store.UpsertAgentConfig(ctx, models.AgentConfig{AgentID: "a1", Model: "m2", UpdatedBy: "bob"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ = store.GetAgentConfig(ctx, "a1")
	if got.Model != "m2" || got.APIKeyCipher != "" || got.APIBaseURL != "" {
		t.Fatalf("replace did not overwrite: %+v", got)
	}
}

func TestMigrateIdempotentWithConfigs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}
