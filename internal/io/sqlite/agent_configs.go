package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"agent-room/internal/models"
)

// GetAgentConfig returns the server-managed startup config for an agent, or
// ErrNotFound when none has been saved. The api key is returned as ciphertext;
// decryption is the caller's responsibility (the relay holds the secret key).
func (s *Store) GetAgentConfig(ctx context.Context, agentID string) (*models.AgentConfig, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT agent_id, model, api_base_url, api_key_cipher, updated_at, updated_by
        FROM agent_configs WHERE agent_id = ?`, agentID)
	return scanAgentConfig(row)
}

// UpsertAgentConfig writes (or replaces) the full config row for an agent. The
// caller passes the complete desired state — partial-update semantics (keep vs
// clear individual fields) are resolved at the API layer before calling this.
func (s *Store) UpsertAgentConfig(ctx context.Context, cfg models.AgentConfig) error {
	updated := cfg.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO agent_configs (agent_id, model, api_base_url, api_key_cipher, updated_at, updated_by)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(agent_id) DO UPDATE SET
            model          = excluded.model,
            api_base_url   = excluded.api_base_url,
            api_key_cipher = excluded.api_key_cipher,
            updated_at     = excluded.updated_at,
            updated_by     = excluded.updated_by`,
		cfg.AgentID,
		cfg.Model,
		cfg.APIBaseURL,
		cfg.APIKeyCipher,
		updated.UTC().Format(time.RFC3339Nano),
		cfg.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("sqlite upsert agent config: %w", err)
	}
	return nil
}

func scanAgentConfig(sc scanner) (*models.AgentConfig, error) {
	var (
		c          models.AgentConfig
		updatedRaw string
	)
	if err := sc.Scan(&c.AgentID, &c.Model, &c.APIBaseURL, &c.APIKeyCipher, &updatedRaw, &c.UpdatedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("sqlite scan agent config: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return nil, fmt.Errorf("sqlite parse agent config updated_at %q: %w", updatedRaw, err)
	}
	c.UpdatedAt = updated
	return &c, nil
}
