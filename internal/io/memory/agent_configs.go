package memory

import (
	"context"
	"time"

	"agent-room/internal/models"
)

// agent_configs mirror the SQLite table so dev runs (AGENT_ROOM_DB_PATH unset)
// exercise the same config read/write + downstream path. Created lazily to keep
// the optional feature out of NewStore.

func (s *Store) ensureAgentConfigMap() {
	if s.agentConfigs == nil {
		s.agentConfigs = make(map[string]models.AgentConfig)
	}
}

func (s *Store) GetAgentConfig(_ context.Context, agentID string) (*models.AgentConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.agentConfigs[agentID]
	if !ok {
		return nil, ErrNotFound
	}
	dup := c
	return &dup, nil
}

func (s *Store) UpsertAgentConfig(_ context.Context, cfg models.AgentConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAgentConfigMap()
	if cfg.UpdatedAt.IsZero() {
		cfg.UpdatedAt = time.Now().UTC()
	}
	s.agentConfigs[cfg.AgentID] = cfg
	return nil
}
