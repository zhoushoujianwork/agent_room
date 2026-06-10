package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agent-room/internal/models"
)

const anonCookieName = "agent_room_anon_id"

// handleAccessRequests dispatches POST/GET/PATCH for /v1/rooms/:id/access-requests[/:rid].
func (s *Server) handleAccessRequests(w http.ResponseWriter, r *http.Request, roomID, requestID string) {
	if s.rooms == nil {
		writeError(w, http.StatusInternalServerError, "no room storage configured")
		return
	}

	switch {
	case requestID == "" && r.Method == http.MethodPost:
		s.handleCreateAccessRequest(w, r, roomID)
	case requestID == "" && r.Method == http.MethodGet:
		s.handleListAccessRequests(w, r, roomID)
	case requestID != "" && r.Method == http.MethodPatch:
		s.handlePatchAccessRequest(w, r, roomID, requestID)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleCreateAccessRequest(w http.ResponseWriter, r *http.Request, roomID string) {
	// Make sure the room exists (lazy-create like GET does).
	if _, err := s.ensureRoom(r.Context(), roomID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var body struct {
		Label string `json:"label"`
	}
	if r.ContentLength != 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	login := s.loginFromRequest(r)
	anonID := requesterAnonID(w, r, login)

	// Already approved? Return the existing record so the client can
	// just enter the room without spamming new requests.
	if existing, err := s.rooms.FindApprovedByRequester(r.Context(), roomID, login, anonID); err == nil && existing != nil {
		writeJSON(w, http.StatusOK, existing)
		return
	}

	// Pending duplicates → 409. This is the de-dupe rule called out in
	// the contract.
	if existing, err := s.rooms.FindPendingByRequester(r.Context(), roomID, login, anonID); err == nil && existing != nil {
		writeJSON(w, http.StatusConflict, existing)
		return
	}

	label := strings.TrimSpace(body.Label)
	via := "anonymous · 房间链接"
	if login != "" {
		via = "GitHub · 房间链接"
		if label == "" {
			label = login
		}
	} else if label == "" {
		label = "viewer-" + shortRandom(4)
	}

	req := models.AccessRequest{
		ID:              "req_" + shortRandom(12),
		RoomID:          roomID,
		RequesterLabel:  label,
		Via:             via,
		Status:          models.AccessRequestStatusPending,
		CreatedAt:       time.Now().UTC(),
		RequesterAnonID: anonID,
	}
	if login != "" {
		l := login
		req.RequesterLogin = &l
	}
	if loc := bestEffortLocation(r); loc != "" {
		l := loc
		req.Location = &l
	}

	if err := s.rooms.CreateAccessRequest(r.Context(), req); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) handleListAccessRequests(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, ok := s.requireOwner(w, r, roomID); !ok {
		return
	}
	list, err := s.rooms.ListAccessRequests(r.Context(), roomID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePatchAccessRequest(w http.ResponseWriter, r *http.Request, roomID, requestID string) {
	if _, ok := s.requireOwner(w, r, roomID); !ok {
		return
	}

	var body struct {
		Decision    string `json:"decision"`
		Persistence string `json:"persistence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req, err := s.rooms.GetAccessRequest(r.Context(), requestID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "request not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.RoomID != roomID {
		writeError(w, http.StatusNotFound, "request not in room")
		return
	}

	switch strings.ToLower(strings.TrimSpace(body.Decision)) {
	case "approve":
		req.Status = models.AccessRequestStatusApproved
		persistence := models.AccessRequestPersistenceOnce
		switch strings.ToLower(strings.TrimSpace(body.Persistence)) {
		case "persist":
			persistence = models.AccessRequestPersistencePersist
		case "once", "":
			persistence = models.AccessRequestPersistenceOnce
		default:
			writeError(w, http.StatusBadRequest, "invalid persistence value")
			return
		}
		req.Persistence = &persistence
	case "deny":
		req.Status = models.AccessRequestStatusDenied
		req.Persistence = nil
	default:
		writeError(w, http.StatusBadRequest, "decision must be approve or deny")
		return
	}
	now := time.Now().UTC()
	req.ResolvedAt = &now

	if err := s.rooms.UpdateAccessRequest(r.Context(), *req); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.hub.broadcastAccessDecision(roomID, *req)
	writeJSON(w, http.StatusOK, req)
}

// requesterAnonID returns the X-Anon-ID header if present, otherwise
// reads (and lazily mints) an HttpOnly cookie. Logged-in users still
// get an anon id assigned so they can be reached by anon-only flows
// (it's harmless; we de-dupe on login first).
func requesterAnonID(w http.ResponseWriter, r *http.Request, login string) string {
	if h := strings.TrimSpace(r.Header.Get("X-Anon-ID")); h != "" {
		return h
	}
	if c, err := r.Cookie(anonCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	// Don't mint anon cookies for signed-in users — their login is the
	// identity. Returning empty here means the de-dupe rule falls back
	// to login alone, which is what we want.
	if login != "" {
		return ""
	}
	id := shortRandom(12)
	http.SetCookie(w, &http.Cookie{
		Name:     anonCookieName,
		Value:    id,
		Path:     "/",
		Expires:  time.Now().Add(180 * 24 * time.Hour),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return id
}

// bestEffortLocation is intentionally a stub right now: we have no
// GeoIP table in the binary. We pass through the requester's IP so
// the contract's "GeoIP city + ip" still partially works.
func bestEffortLocation(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
		// Take the first hop in the chain.
		if idx := strings.IndexByte(v, ','); idx > 0 {
			return strings.TrimSpace(v[:idx])
		}
		return v
	}
	return r.RemoteAddr
}

func shortRandom(byteLen int) string {
	if byteLen <= 0 {
		byteLen = 8
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is exceptional; fall back to a fixed
		// string rather than panicking the relay.
		return fmt.Sprintf("fallback%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
