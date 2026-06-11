package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"agent-room/internal/models"
)

// The in-memory store mirrors the SQLite agent/token tables so dev runs
// (AGENT_ROOM_DB_PATH unset) exercise the same binding flow. Maps are created
// lazily to avoid touching NewStore for this optional feature.

func (s *Store) ensureAgentMaps() {
	if s.agents == nil {
		s.agents = make(map[string]models.Agent)
	}
	if s.agentTokens == nil {
		s.agentTokens = make(map[string]models.AgentToken)
	}
}

func (s *Store) UpsertAgent(_ context.Context, agent models.Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAgentMaps()
	now := agent.LastSeenAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	existing, ok := s.agents[agent.AgentID]
	if !ok {
		created := agent.CreatedAt
		if created.IsZero() {
			created = now
		}
		agent.CreatedAt = created
		agent.LastSeenAt = now
		agent.Revoked = false
		s.agents[agent.AgentID] = agent
		return nil
	}
	existing.OwnerLogin = agent.OwnerLogin
	if strings.TrimSpace(agent.Label) != "" {
		existing.Label = agent.Label
	}
	if strings.TrimSpace(agent.Provider) != "" {
		existing.Provider = agent.Provider
	}
	existing.LastSeenAt = now
	existing.Revoked = false
	s.agents[agent.AgentID] = existing
	return nil
}

func (s *Store) GetAgent(_ context.Context, agentID string) (*models.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[agentID]
	if !ok {
		return nil, ErrNotFound
	}
	c := a
	return &c, nil
}

func (s *Store) ListAgentsByOwner(_ context.Context, owner string) ([]models.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Agent, 0)
	for _, a := range s.agents {
		if a.Revoked || a.OwnerLogin != owner {
			continue
		}
		out = append(out, a)
	}
	sortAgents(out)
	return out, nil
}

func (s *Store) ListAllAgents(_ context.Context) ([]models.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Agent, 0)
	for _, a := range s.agents {
		if a.Revoked {
			continue
		}
		out = append(out, a)
	}
	sortAgents(out)
	return out, nil
}

func (s *Store) RevokeAgent(_ context.Context, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok {
		return ErrNotFound
	}
	a.Revoked = true
	s.agents[agentID] = a
	return nil
}

func (s *Store) InsertAgentToken(_ context.Context, token models.AgentToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAgentMaps()
	if token.CreatedAt.IsZero() {
		token.CreatedAt = time.Now().UTC()
	}
	token.LastUsedAt = nil
	token.Revoked = false
	s.agentTokens[token.TokenHash] = token
	return nil
}

func (s *Store) LookupAgentToken(_ context.Context, hash string) (*models.AgentToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.agentTokens[hash]
	if !ok {
		return nil, ErrNotFound
	}
	c := t
	return &c, nil
}

func (s *Store) TouchAgentTokenLastUsed(_ context.Context, hash string, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.agentTokens[hash]
	if !ok {
		return ErrNotFound
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	t.LastUsedAt = &ts
	s.agentTokens[hash] = t
	return nil
}

func (s *Store) ListAgentTokensByOwner(_ context.Context, owner string) ([]models.AgentToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.AgentToken, 0)
	for _, t := range s.agentTokens {
		if t.Revoked || t.OwnerLogin != owner {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) RevokeAgentTokenByPrefix(_ context.Context, owner, prefix string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Collect matching hashes first; only revoke if exactly one matches.
	var matches []string
	for hash, t := range s.agentTokens {
		if t.Revoked || t.OwnerLogin != owner || !strings.HasPrefix(hash, prefix) {
			continue
		}
		matches = append(matches, hash)
	}
	if len(matches) != 1 {
		// 0 → not found; >1 → ambiguous prefix — do not revoke anything.
		return len(matches), nil
	}
	t := s.agentTokens[matches[0]]
	t.Revoked = true
	s.agentTokens[matches[0]] = t
	return 1, nil
}

func sortAgents(agents []models.Agent) {
	sort.Slice(agents, func(i, j int) bool { return agents[i].LastSeenAt.After(agents[j].LastSeenAt) })
}

// AddAgentRoom records that agentID should be in roomID (desired state). Idempotent.
func (s *Store) AddAgentRoom(_ context.Context, agentID, roomID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentRooms == nil {
		s.agentRooms = make(map[string]map[string]struct{})
	}
	if s.agentRooms[agentID] == nil {
		s.agentRooms[agentID] = make(map[string]struct{})
	}
	s.agentRooms[agentID][roomID] = struct{}{}
	return nil
}

// RemoveAgentRoom removes roomID from agentID's desired state. No error if not found.
func (s *Store) RemoveAgentRoom(_ context.Context, agentID, roomID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentRooms != nil {
		delete(s.agentRooms[agentID], roomID)
	}
	return nil
}

// ListAgentRooms returns the desired room list for an agent.
func (s *Store) ListAgentRooms(_ context.Context, agentID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.agentRooms[agentID]
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}
