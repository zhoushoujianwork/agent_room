package relay

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"agent-room/internal/models"
)

// handleRoomAgents dispatches POST /v1/rooms/{roomID}/agents and
// DELETE /v1/rooms/{roomID}/agents/{agentID}.
func (s *Server) handleRoomAgents(w http.ResponseWriter, r *http.Request, roomID, agentID string) {
	switch {
	case agentID == "" && r.Method == http.MethodPost:
		s.handleAddAgentToRoom(w, r, roomID)
	case agentID != "" && r.Method == http.MethodDelete:
		s.handleRemoveAgentFromRoom(w, r, roomID, agentID)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAddAgentToRoom serves POST /v1/rooms/{roomID}/agents.
func (s *Server) handleAddAgentToRoom(w http.ResponseWriter, r *http.Request, roomID string) {
	login := s.requireSession(w, r)
	if login == "" {
		return
	}
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, "agent storage not configured")
		return
	}

	var body struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	agentID := strings.TrimSpace(body.AgentID)
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id required")
		return
	}

	// Validate agent ownership.
	agent, err := s.agents.GetAgent(r.Context(), agentID)
	if err != nil || agent == nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if agent.Revoked {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if agent.OwnerLogin != login && !s.isAdminRequest(r) {
		writeError(w, http.StatusForbidden, "not your agent")
		return
	}

	// Validate room existence and state.
	if s.rooms == nil {
		writeError(w, http.StatusInternalServerError, "room storage not configured")
		return
	}
	room, err := s.rooms.GetRoom(r.Context(), roomID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "room not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if room.Ended {
		writeError(w, http.StatusConflict, "room has ended")
		return
	}

	// Gated check: only room owner or admin may add agents to gated rooms.
	if room.Gated {
		isOwner := room.OwnerLogin != nil && *room.OwnerLogin == login
		if !isOwner && !s.isAdminRequest(r) {
			writeError(w, http.StatusForbidden, "room is gated")
			return
		}
	}

	// Persist desired state.
	if err := s.agents.AddAgentRoom(r.Context(), agentID, roomID, login); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Deliver join_room via control connection if online.
	msg := models.ChatMessage{
		Type:       models.MessageTypeControl,
		SenderKind: models.SenderKindSystem,
		TargetID:   agentID,
		CreatedAt:  time.Now().UTC(),
		Metadata: map[string]string{
			"operation": models.ControlOperationJoinRoom,
			"room_id":   roomID,
		},
	}
	delivered := s.hub.sendToAgentCtrl(agentID, msg)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delivered": delivered})
}

// handleRemoveAgentFromRoom serves DELETE /v1/rooms/{roomID}/agents/{agentID}.
func (s *Server) handleRemoveAgentFromRoom(w http.ResponseWriter, r *http.Request, roomID, agentID string) {
	login := s.requireSession(w, r)
	if login == "" {
		return
	}
	if s.agents == nil {
		writeError(w, http.StatusInternalServerError, "agent storage not configured")
		return
	}

	// Resolve agent.
	agent, err := s.agents.GetAgent(r.Context(), agentID)
	if err != nil || agent == nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	// Check authorization: agent owner, room owner, or admin.
	allowed := false
	if agent.OwnerLogin == login {
		allowed = true
	}
	if s.isAdminRequest(r) {
		allowed = true
	}
	if !allowed && s.rooms != nil {
		room, rerr := s.rooms.GetRoom(r.Context(), roomID)
		if rerr == nil && room != nil && room.OwnerLogin != nil && *room.OwnerLogin == login {
			allowed = true
		}
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "not authorized")
		return
	}

	if err := s.agents.RemoveAgentRoom(r.Context(), agentID, roomID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Deliver leave_room via control connection if online.
	msg := models.ChatMessage{
		Type:       models.MessageTypeControl,
		SenderKind: models.SenderKindSystem,
		TargetID:   agentID,
		CreatedAt:  time.Now().UTC(),
		Metadata: map[string]string{
			"operation": models.ControlOperationLeaveRoom,
			"room_id":   roomID,
		},
	}
	delivered := s.hub.sendToAgentCtrl(agentID, msg)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delivered": delivered})
}
