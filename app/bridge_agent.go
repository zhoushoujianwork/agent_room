package app

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"agent-room/internal/models"
	"agent-room/internal/service/agent"
	"agent-room/internal/service/chat"
	"agent-room/internal/version"
	"agent-room/pkg/id"
)

// maxPendingOutbound caps the per-room disconnect buffer. A single generation
// emits dozens of trace events (the worst stuck case we saw had 18), so this
// leaves ample headroom; on overflow we drop the OLDEST entry, which is always
// an intermediate trace — the reply and terminal trace are the newest and survive.
const maxPendingOutbound = 256

// roomConn holds one room's connection state. The agentEngine owns a map of
// these, one per room currently being served. Each roomConn has its own
// reconnect loop goroutine and outbound buffer.
type roomConn struct {
	roomID string

	mu      sync.Mutex
	conn    *websocket.Conn      // nil = disconnected
	pending []models.ChatMessage // outbound buffer while disconnected

	cancel context.CancelFunc // stops this room's reconnect loop
}

// send writes msg to the room connection, or buffers it while disconnected.
// gorilla/websocket forbids concurrent writes; all writes to rc.conn serialise
// under rc.mu.
func (rc *roomConn) send(msg models.ChatMessage) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.conn == nil {
		rc.bufferLocked(msg)
		return
	}
	if err := rc.conn.WriteJSON(msg); err != nil {
		rc.conn = nil
		rc.bufferLocked(msg)
	}
}

func (rc *roomConn) bufferLocked(msg models.ChatMessage) {
	if len(rc.pending) >= maxPendingOutbound {
		rc.pending = rc.pending[1:]
	}
	rc.pending = append(rc.pending, msg)
}

// attach installs conn as the write sink and flushes buffered messages.
func (rc *roomConn) attach(conn *websocket.Conn) {
	rc.mu.Lock()
	rc.conn = conn
	pending := rc.pending
	rc.pending = nil
	rc.mu.Unlock()

	for _, msg := range pending {
		rc.send(msg)
	}
}

// detach clears conn when the connection closes, but only if conn still matches
// (a racing reconnect may have already installed a newer conn).
func (rc *roomConn) detach(conn *websocket.Conn) {
	rc.mu.Lock()
	if rc.conn == conn {
		rc.conn = nil
	}
	rc.mu.Unlock()
}

// agentEngine owns the agent bridge's generation lifecycle independently of any
// single websocket connection. A reader goroutine (per connection) classifies
// inbound messages — dispatching stop signals and queuing reply jobs — while a
// single long-lived worker goroutine runs replies serially (one `claude -p` at
// a time).
//
// Multi-room support: when an agent_token is configured the engine maintains a
// map of roomConn values, one per room currently joined. Outbound messages are
// routed by msg.RoomID to the correct roomConn. A separate control connection
// to /v1/agents/ws receives join_room / leave_room directives from the relay.
// Without a token the engine behaves exactly as before: a single roomConn,
// no control connection.
type agentEngine struct {
	app       *BridgeApp
	responder *agent.Responder
	chat      *chat.Service
	provider  models.AgentProvider
	label     string
	httpBase  string

	// engineCtx is the parent of every generation context. Cancelled only when
	// the bridge process shuts down, never by a connection ending, so an
	// in-flight reply survives a reconnect.
	engineCtx context.Context
	jobs      chan models.ChatMessage

	// roomsMu guards the rooms map.
	roomsMu sync.Mutex
	rooms   map[string]*roomConn

	// genMu guards the in-flight generation handle below.
	genMu          sync.Mutex
	currentCancel  context.CancelFunc
	currentReplyTo string

	// permMu guards the pending-permission registry below. The reverse
	// approval channel is the structural mirror of the stop signal: generate()
	// blocks in a worker goroutine awaiting a decision while readLoop, in
	// another goroutine, delivers it by permission_id.
	permMu      sync.Mutex
	pendingPerm map[string]chan models.PermissionDecision

	// execMu guards the pending-exec-delegation registry below.
	execMu      sync.Mutex
	pendingExec map[string]chan models.ChatMessage

	// controlMu serialises writes on the control connection.
	controlMu   sync.Mutex
	controlConn *websocket.Conn
}

// send routes msg to the roomConn identified by msg.RoomID. If no roomConn
// exists for that room the message is dropped with a warning (can happen
// transiently during leave_room).
func (e *agentEngine) send(msg models.ChatMessage) error {
	e.roomsMu.Lock()
	rc := e.rooms[msg.RoomID]
	e.roomsMu.Unlock()
	if rc == nil {
		e.app.logger.Warn("send: no room conn for room; dropping",
			slog.String("room_id", msg.RoomID))
		return nil
	}
	rc.send(msg)
	return nil
}

// roomIDList returns the current set of room ids as a comma-separated string.
func (e *agentEngine) roomIDList() string {
	e.roomsMu.Lock()
	defer e.roomsMu.Unlock()
	ids := make([]string, 0, len(e.rooms))
	for k := range e.rooms {
		ids = append(ids, k)
	}
	return strings.Join(ids, ",")
}

// sendRoomStateReport sends a room_state_report over the control connection.
// Safe to call when no control connection is active (no-op).
func (e *agentEngine) sendRoomStateReport() {
	rooms := e.roomIDList()
	msg := models.ChatMessage{
		Type:       models.MessageTypeControl,
		SenderID:   e.app.cfg.AgentID,
		SenderKind: models.SenderKindAgent,
		CreatedAt:  time.Now().UTC(),
		Metadata: map[string]string{
			"operation": models.ControlOperationRoomStateReport,
			"rooms":     rooms,
		},
	}
	e.controlMu.Lock()
	defer e.controlMu.Unlock()
	if e.controlConn != nil {
		_ = e.controlConn.WriteJSON(msg)
	}
}

// joinRoom idempotently creates and starts a room connection. Safe to call
// concurrently (e.g. from both a local -room flag and a join_room directive).
func (e *agentEngine) joinRoom(ctx context.Context, roomID string) {
	e.roomsMu.Lock()
	if _, exists := e.rooms[roomID]; exists {
		e.roomsMu.Unlock()
		return // idempotent
	}
	roomCtx, cancel := context.WithCancel(ctx)
	rc := &roomConn{roomID: roomID, cancel: cancel}
	if e.rooms == nil {
		e.rooms = make(map[string]*roomConn)
	}
	e.rooms[roomID] = rc
	e.roomsMu.Unlock()

	go e.runRoomConn(roomCtx, rc)
}

// leaveRoom cancels a room connection loop and removes it from the map.
func (e *agentEngine) leaveRoom(roomID string) {
	e.roomsMu.Lock()
	rc, exists := e.rooms[roomID]
	if exists {
		delete(e.rooms, roomID)
	}
	e.roomsMu.Unlock()
	if !exists {
		return
	}
	rc.cancel()
	rc.mu.Lock()
	conn := rc.conn
	rc.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// runRoomConn is the per-room reconnect loop. It mirrors runWithReconnect but
// constructs a room-specific URL and uses the shared agentEngine as handler.
func (e *agentEngine) runRoomConn(ctx context.Context, rc *roomConn) {
	a := e.app
	roomURL := buildRoomURL(a.cfg.RelayURL, rc.roomID, a.cfg.AgentToken)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		conn, _, dialErr := websocket.DefaultDialer.DialContext(ctx, roomURL, nil)
		if dialErr != nil {
			if ctx.Err() != nil {
				return
			}
			a.logger.Warn("room connect failed; retrying",
				slog.String("room_id", rc.roomID),
				slog.Duration("retry_after", backoff),
				slog.Any("error", dialErr))
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
		a.logger.Info("room connected",
			slog.String("room_id", rc.roomID),
			slog.String("agent_id", a.cfg.AgentID))

		rc.attach(conn)
		stopClose := closeConnOnDone(ctx, conn)
		stopPing := keepAlive(ctx, conn)
		err := e.serveRoomConn(ctx, rc.roomID, conn)
		stopPing()
		stopClose()
		rc.detach(conn)
		_ = conn.Close()

		if ctx.Err() != nil {
			return
		}
		a.logger.Warn("room disconnected; reconnecting",
			slog.String("room_id", rc.roomID),
			slog.Duration("retry_after", backoff),
			slog.Any("error", err))
		if !sleepContext(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

// buildRoomURL constructs the ws URL for a specific room. It takes the base
// relay URL (which may already contain a different room path) and replaces the
// room segment, preserving query params.
func buildRoomURL(baseRelayURL, roomID, token string) string {
	u, err := url.Parse(baseRelayURL)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = "/v1/rooms/" + url.PathEscape(roomID) + "/ws"
	if token != "" {
		q := u.Query()
		q.Set("agent_token", token)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// agentControlURL builds the control connection URL: /v1/agents/ws with the
// agent_token, client_id, client_kind, and client_label query params.
func agentControlURL(baseRelayURL, agentID, label, token string) string {
	u, err := url.Parse(baseRelayURL)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = "/v1/agents/ws"
	q := u.Query()
	q.Del("agent_token")
	q.Set("agent_token", token)
	q.Set("client_id", agentID)
	q.Set("client_kind", "agent")
	if label != "" {
		q.Set("client_label", label)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// runControlConn runs the control connection reconnect loop.
// It connects to /v1/agents/ws, sends room_state_report on each (re)connect,
// and processes join_room / leave_room / config_update directives from the relay.
func (e *agentEngine) runControlConn(ctx context.Context) {
	a := e.app
	ctrlURL := agentControlURL(a.cfg.RelayURL, a.cfg.AgentID, e.label, a.cfg.AgentToken)
	if ctrlURL == "" {
		a.logger.Warn("control: could not build control URL; skipping")
		return
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, ctrlURL, nil)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.logger.Warn("control connect failed; retrying",
				slog.Duration("retry_after", backoff),
				slog.Any("error", err))
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
		a.logger.Info("control connected", slog.String("agent_id", a.cfg.AgentID))

		// Register active control connection so sendRoomStateReport can use it.
		e.controlMu.Lock()
		e.controlConn = conn
		e.controlMu.Unlock()

		stopClose := closeConnOnDone(ctx, conn)
		stopPing := keepAlive(ctx, conn)

		// Report current rooms right after connect.
		e.sendRoomStateReport()

		// Control read loop.
		e.readControlLoop(ctx, conn)

		stopPing()
		stopClose()
		_ = conn.Close()

		e.controlMu.Lock()
		if e.controlConn == conn {
			e.controlConn = nil
		}
		e.controlMu.Unlock()

		if ctx.Err() != nil {
			return
		}
		a.logger.Warn("control disconnected; reconnecting",
			slog.Duration("retry_after", backoff))
		if !sleepContext(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

// readControlLoop processes messages on the control connection. It handles only
// join_room, leave_room, and config_update — no room history or chat.Add.
func (e *agentEngine) readControlLoop(ctx context.Context, conn *websocket.Conn) {
	a := e.app
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var msg models.ChatMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if ctx.Err() != nil {
				return
			}
			a.logger.Warn("control read error", slog.Any("error", err))
			return
		}

		op := strings.TrimSpace(msg.Metadata["operation"])
		switch op {
		case models.ControlOperationJoinRoom:
			roomID := strings.TrimSpace(msg.Metadata["room_id"])
			if roomID == "" {
				a.logger.Warn("control: join_room missing room_id")
				continue
			}
			a.logger.Info("control: join_room", slog.String("room_id", roomID))
			e.joinRoom(ctx, roomID)
			e.sendRoomStateReport()

		case models.ControlOperationLeaveRoom:
			roomID := strings.TrimSpace(msg.Metadata["room_id"])
			if roomID == "" {
				a.logger.Warn("control: leave_room missing room_id")
				continue
			}
			a.logger.Info("control: leave_room", slog.String("room_id", roomID))
			e.leaveRoom(roomID)
			e.sendRoomStateReport()

		case models.ControlOperationConfigUpdate:
			// Strip api_key before any storage; control conn msgs don't go to chat store.
			if strings.TrimSpace(msg.Metadata["api_key"]) != "" {
				msg.Metadata = copyMetadataWithout(msg.Metadata, "api_key")
			}
			e.applyConfigUpdate(msg)

		default:
			a.logger.Debug("control: unknown operation; ignoring", slog.String("op", op))
		}
	}
}

// shouldCancel decides whether a control/stop message targets the in-flight
// generation. A stop with no reply_to cancels whatever is currently running;
// a stop naming a reply_to only matches when it equals the generation that
// reply_to triggered, so connecting two messages in quick succession can't
// stop the wrong one. currentReplyTo == "" means nothing is in flight.
func shouldCancel(msg models.ChatMessage, currentReplyTo string) bool {
	if currentReplyTo == "" {
		return false
	}
	if msg.Type != models.MessageTypeControl {
		return false
	}
	if strings.TrimSpace(msg.Metadata["operation"]) != models.ControlOperationStop {
		return false
	}
	wantReplyTo := strings.TrimSpace(msg.Metadata["reply_to"])
	if wantReplyTo == "" {
		return true
	}
	return wantReplyTo == currentReplyTo
}

// registerGeneration records the cancel func for the reply triggered by the
// given message id, so a later stop signal can interrupt it.
func (e *agentEngine) registerGeneration(replyTo string, cancel context.CancelFunc) {
	e.genMu.Lock()
	defer e.genMu.Unlock()
	e.currentCancel = cancel
	e.currentReplyTo = replyTo
}

// clearGeneration removes the in-flight handle (after a reply finishes) and
// cancels its context to release resources. Safe to call repeatedly.
func (e *agentEngine) clearGeneration() {
	e.genMu.Lock()
	cancel := e.currentCancel
	e.currentCancel = nil
	e.currentReplyTo = ""
	e.genMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// maybeCancel cancels the in-flight generation if msg is a stop targeting it.
// Returns true when a cancellation was triggered.
func (e *agentEngine) maybeCancel(msg models.ChatMessage) bool {
	e.genMu.Lock()
	defer e.genMu.Unlock()
	if e.currentCancel == nil || !shouldCancel(msg, e.currentReplyTo) {
		return false
	}
	e.currentCancel()
	return true
}

// registerPermission records a pending approval channel keyed by permission id
// and returns the receive end. The buffered channel (cap 1) lets resolve land
// even if generate() is momentarily not selecting yet.
func (e *agentEngine) registerPermission(permID string) <-chan models.PermissionDecision {
	ch := make(chan models.PermissionDecision, 1)
	e.permMu.Lock()
	if e.pendingPerm == nil {
		e.pendingPerm = make(map[string]chan models.PermissionDecision)
	}
	e.pendingPerm[permID] = ch
	e.permMu.Unlock()
	return ch
}

// clearPermission removes a pending approval entry. Safe to call repeatedly.
func (e *agentEngine) clearPermission(permID string) {
	e.permMu.Lock()
	delete(e.pendingPerm, permID)
	e.permMu.Unlock()
}

// resolvePermission delivers a decision to a waiting RequestPermission closure.
// Returns true when a matching pending request existed.
func (e *agentEngine) resolvePermission(permID string, decision models.PermissionDecision) bool {
	e.permMu.Lock()
	ch, ok := e.pendingPerm[permID]
	if ok {
		delete(e.pendingPerm, permID)
	}
	e.permMu.Unlock()
	if !ok {
		return false
	}
	ch <- decision
	return true
}

// registerExec records a pending exec-delegation channel keyed by command id.
func (e *agentEngine) registerExec(commandID string) <-chan models.ChatMessage {
	ch := make(chan models.ChatMessage, 1)
	e.execMu.Lock()
	if e.pendingExec == nil {
		e.pendingExec = make(map[string]chan models.ChatMessage)
	}
	e.pendingExec[commandID] = ch
	e.execMu.Unlock()
	return ch
}

// clearExec removes a pending exec-delegation entry. Safe to call repeatedly.
func (e *agentEngine) clearExec(commandID string) {
	e.execMu.Lock()
	delete(e.pendingExec, commandID)
	e.execMu.Unlock()
}

// resolveExec delivers a command_result to a waiting delegateExec closure.
func (e *agentEngine) resolveExec(commandID string, result models.ChatMessage) bool {
	e.execMu.Lock()
	ch, ok := e.pendingExec[commandID]
	if ok {
		delete(e.pendingExec, commandID)
	}
	e.execMu.Unlock()
	if !ok {
		return false
	}
	ch <- result
	return true
}

// start launches the single long-lived worker goroutine. The worker outlives
// individual connections, draining e.jobs under engineCtx.
func (e *agentEngine) start() {
	e.jobs = make(chan models.ChatMessage, 8)
	go e.worker()
}

// serveConn is the legacy single-room entry point used when no agent_token is
// configured. It wraps serveRoomConn using the configured cfg.RoomID.
func (e *agentEngine) serveConn(ctx context.Context, conn *websocket.Conn) error {
	a := e.app
	// In legacy (no-token) mode the engine has exactly one roomConn which was
	// created by runAgent before the first connect. We need to attach/detach it.
	e.roomsMu.Lock()
	rc := e.rooms[a.cfg.RoomID]
	e.roomsMu.Unlock()
	if rc == nil {
		// Should not happen; guard defensively.
		return errors.New("serveConn: no roomConn for legacy room")
	}
	rc.attach(conn)
	defer rc.detach(conn)
	return e.serveRoomConn(ctx, a.cfg.RoomID, conn)
}

// serveRoomConn handles one websocket connection for a given room: sends
// presence, seeds history, then runs the read loop.
func (e *agentEngine) serveRoomConn(ctx context.Context, roomID string, conn *websocket.Conn) error {
	a := e.app

	if err := conn.WriteJSON(models.ChatMessage{
		RoomID:     roomID,
		Type:       models.MessageTypePresence,
		SenderID:   a.cfg.AgentID,
		SenderKind: models.SenderKindAgent,
		Content:    "agent joined",
		CreatedAt:  time.Now().UTC(),
		Metadata: map[string]string{
			"provider":     e.provider.Name(),
			"label":        e.label,
			"capabilities": a.cfg.Capabilities,
			"os":           runtime.GOOS,
			"version":      version.String(),
		},
	}); err != nil {
		return err
	}

	// Seed local history from the relay. Best-effort.
	a.seedRoomHistory(ctx, e.chat, e.httpBase, roomID)

	return e.readLoop(ctx, roomID, conn)
}

// readLoop reads inbound messages from conn, stores them, and dispatches stop
// signals or queues reply jobs. It returns when the connection closes or ctx
// is done.
func (e *agentEngine) readLoop(ctx context.Context, roomID string, conn *websocket.Conn) error {
	a := e.app
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		var msg models.ChatMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		// Strip api_key before storing.
		stored := msg
		if msg.Type == models.MessageTypeControl &&
			strings.TrimSpace(msg.Metadata["operation"]) == models.ControlOperationConfigUpdate &&
			strings.TrimSpace(msg.Metadata["api_key"]) != "" {
			stored.Metadata = copyMetadataWithout(msg.Metadata, "api_key")
		}
		if _, err := e.chat.Add(ctx, stored.RoomID, stored); err != nil {
			a.logger.Warn("store incoming message failed", slog.Any("error", err))
		}

		if msg.Type == models.MessageTypeControl && msg.IsAddressedTo(a.cfg.AgentID) {
			switch strings.TrimSpace(msg.Metadata["operation"]) {
			case models.ControlOperationConfigUpdate:
				e.applyConfigUpdate(msg)
			case models.ControlOperationPermissionReply:
				permID := strings.TrimSpace(msg.Metadata["permission_id"])
				reply := models.PermissionReply(strings.TrimSpace(msg.Metadata["reply"]))
				patterns := splitPatterns(msg.Metadata["patterns"])
				if permID != "" && e.resolvePermission(permID, models.PermissionDecision{
					Reply:    reply,
					By:       msg.SenderID,
					Reason:   strings.TrimSpace(msg.Metadata["reason"]),
					Patterns: patterns,
				}) {
					a.logger.Info("permission decision delivered",
						slog.String("room_id", msg.RoomID),
						slog.String("from", msg.SenderID),
						slog.String("permission_id", permID),
						slog.String("reply", string(reply)),
						slog.String("patterns", strings.Join(patterns, " | ")),
					)
				}
			default:
				if e.maybeCancel(msg) {
					a.logger.Info("stop signal honored",
						slog.String("room_id", msg.RoomID),
						slog.String("from", msg.SenderID),
						slog.String("reply_to", strings.TrimSpace(msg.Metadata["reply_to"])),
					)
				}
			}
			continue
		}

		if msg.Type == models.MessageTypeCommandResult && msg.IsAddressedTo(a.cfg.AgentID) {
			cmdID := strings.TrimSpace(msg.Metadata["command_id"])
			if cmdID != "" && e.resolveExec(cmdID, msg) {
				a.logger.Info("delegated exec result delivered",
					slog.String("room_id", msg.RoomID),
					slog.String("from", msg.SenderID),
					slog.String("command_id", cmdID),
				)
			}
			continue
		}

		if !e.responder.ShouldReply(msg) {
			continue
		}

		// Non-blocking enqueue with queue-feedback: inform the sender if busy
		// or the queue is full, rather than silently dropping.
		e.enqueueWithFeedback(msg)
	}
}

// enqueueWithFeedback attempts to queue msg for the worker. If the worker is
// currently busy it sends a "queued" trace to the originating room. If the
// queue is full it sends a "queue full" trace and drops the message.
func (e *agentEngine) enqueueWithFeedback(msg models.ChatMessage) {
	a := e.app

	// Check whether the worker is currently busy.
	e.genMu.Lock()
	busy := e.currentReplyTo != ""
	queueLen := len(e.jobs)
	e.genMu.Unlock()

	select {
	case e.jobs <- msg:
		if busy {
			// Worker has something in flight; tell the sender they are queued.
			_ = e.send(models.ChatMessage{
				RoomID:     msg.RoomID,
				Type:       models.MessageTypeTrace,
				SenderID:   a.cfg.AgentID,
				SenderKind: models.SenderKindAgent,
				TargetID:   msg.SenderID,
				Content:    "已排队（前方还有 " + strconv.Itoa(queueLen+1) + " 个请求）",
				CreatedAt:  time.Now().UTC(),
				Metadata: map[string]string{
					"phase":    "queued",
					"reply_to": msg.ID,
					"provider": e.provider.Name(),
					"label":    e.label,
				},
			})
		}
	default:
		a.logger.Warn("reply queue full; dropping message",
			slog.String("room_id", msg.RoomID), slog.String("from", msg.SenderID))
		_ = e.send(models.ChatMessage{
			RoomID:     msg.RoomID,
			Type:       models.MessageTypeTrace,
			SenderID:   a.cfg.AgentID,
			SenderKind: models.SenderKindAgent,
			TargetID:   msg.SenderID,
			Content:    "请求队列已满，请稍后重试",
			CreatedAt:  time.Now().UTC(),
			Metadata: map[string]string{
				"phase":    "queue_full",
				"reply_to": msg.ID,
				"provider": e.provider.Name(),
				"label":    e.label,
			},
		})
	}
}

// runtimeConfigurable is implemented by providers that accept a server-pushed
// startup config (model / api base url / api key) at runtime.
type runtimeConfigurable interface {
	ApplyServerConfig(model, apiBaseURL, apiKey string)
}

// applyConfigUpdate applies a relay config_update control message to the local
// provider's in-memory runtime config.
func (e *agentEngine) applyConfigUpdate(msg models.ChatMessage) {
	configurable, ok := e.provider.(runtimeConfigurable)
	if !ok {
		e.app.logger.Warn("config_update ignored: provider not runtime-configurable",
			slog.String("provider", e.provider.Name()))
		return
	}
	model := strings.TrimSpace(msg.Metadata["model"])
	apiBaseURL := strings.TrimSpace(msg.Metadata["api_base_url"])
	apiKey := msg.Metadata["api_key"] // may be empty; never logged
	configurable.ApplyServerConfig(model, apiBaseURL, apiKey)
	e.app.logger.Info("agent config updated from relay",
		slog.String("room_id", msg.RoomID),
		slog.String("model", model),
		slog.String("api_base_url", apiBaseURL),
		slog.Bool("api_key_set", strings.TrimSpace(apiKey) != ""))
}

// copyMetadataWithout returns a shallow copy of metadata with the named key
// removed.
func copyMetadataWithout(metadata map[string]string, drop string) map[string]string {
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if k == drop {
			continue
		}
		out[k] = v
	}
	return out
}

// worker runs queued replies one at a time, for the lifetime of the engine.
func (e *agentEngine) worker() {
	for {
		select {
		case <-e.engineCtx.Done():
			return
		case msg, ok := <-e.jobs:
			if !ok {
				return
			}
			if e.engineCtx.Err() != nil {
				return
			}
			e.generate(msg)
		}
	}
}

func (e *agentEngine) generate(msg models.ChatMessage) {
	a := e.app
	genCtx, cancel := context.WithCancel(e.engineCtx)
	e.registerGeneration(msg.ID, cancel)
	defer e.clearGeneration()

	history := a.replyHistory(genCtx, e.chat, msg.RoomID)
	roomSummary := fetchRoomSummary(genCtx, e.httpBase, msg.RoomID)

	a.logger.Info("generating reply",
		slog.String("room_id", msg.RoomID),
		slog.String("from", msg.SenderID),
		slog.Int("turn_budget", msg.TurnBudget),
		slog.Int("history_msgs", len(history)),
		slog.Bool("has_summary", roomSummary != ""),
		slog.Int("summary_chars", len(roomSummary)),
	)
	startedAt := time.Now().UTC()
	_ = e.send(e.trace(msg, "thinking", "thinking", startedAt, nil))

	onEvent := func(ev models.ProviderEvent) {
		if ev.Type == models.ProviderEventPermissionRequest {
			return
		}
		extra := map[string]string{"event_type": string(ev.Type)}
		if ev.Tool != "" {
			extra["tool"] = ev.Tool
		}
		if ev.Detail != "" {
			extra["detail"] = ev.Detail
		}
		for k, v := range ev.Metadata {
			if v != "" {
				extra[k] = v
			}
		}
		if err := e.send(e.trace(msg, string(ev.Type), ev.Summary, time.Now().UTC(), extra)); err != nil {
			a.logger.Warn("write trace event failed", slog.Any("error", err))
		}
	}

	requestPermission := func(ctx context.Context, p models.PermissionRequest) (models.PermissionDecision, error) {
		permID := strings.TrimSpace(p.RequestID)
		if permID == "" {
			permID = id.New("perm")
		}
		ch := e.registerPermission(permID)
		defer e.clearPermission(permID)

		extra := map[string]string{
			"permission_id": permID,
			"tool":          p.Tool,
			"input":         p.Input,
		}
		if p.Pattern != "" {
			extra["pattern"] = p.Pattern
		}
		for k, v := range p.Metadata {
			if v != "" {
				extra[k] = v
			}
		}
		if err := e.send(e.trace(msg, "permission_request", "permission requested", time.Now().UTC(), extra)); err != nil {
			a.logger.Warn("write permission request failed", slog.Any("error", err))
		}
		askedAt := time.Now()
		a.logger.Info("permission requested",
			slog.String("room_id", msg.RoomID),
			slog.String("permission_id", permID),
			slog.String("tool", p.Tool),
			slog.String("input", truncateForLog(p.Input, 200)),
			slog.String("pattern", p.Pattern),
		)

		select {
		case <-ctx.Done():
			a.logger.Info("permission wait aborted",
				slog.String("room_id", msg.RoomID),
				slog.String("permission_id", permID),
				slog.Duration("waited", time.Since(askedAt)),
			)
			return models.PermissionDecision{}, ctx.Err()
		case decision := <-ch:
			a.logger.Info("permission resolved",
				slog.String("room_id", msg.RoomID),
				slog.String("permission_id", permID),
				slog.String("tool", p.Tool),
				slog.String("reply", string(decision.Reply)),
				slog.String("by", decision.By),
				slog.Duration("waited", time.Since(askedAt)),
			)
			return decision, nil
		}
	}

	delegateExec := func(ctx context.Context, dreq models.DelegateExecRequest) (models.DelegateExecResult, error) {
		target := strings.TrimSpace(dreq.TargetID)
		command := strings.TrimSpace(dreq.Command)
		if target == "" || command == "" {
			return models.DelegateExecResult{}, errors.New("delegate exec: target_id and command are required")
		}

		commandID := id.New("msg")
		ch := e.registerExec(commandID)
		defer e.clearExec(commandID)

		meta := map[string]string{"operation": "exec"}
		if dreq.TimeoutMS > 0 {
			meta["timeout_ms"] = strconv.Itoa(dreq.TimeoutMS)
		}
		if dreq.Cwd != "" {
			meta["cwd"] = dreq.Cwd
		}
		if dreq.Shell != "" {
			meta["shell"] = dreq.Shell
		}
		cmdMsg := models.ChatMessage{
			ID:         commandID,
			RoomID:     msg.RoomID,
			Type:       models.MessageTypeCommand,
			SenderID:   a.cfg.AgentID,
			SenderKind: models.SenderKindAgent,
			TargetID:   target,
			Content:    command,
			CreatedAt:  time.Now().UTC(),
			Metadata:   meta,
		}
		if err := e.send(cmdMsg); err != nil {
			return models.DelegateExecResult{}, err
		}
		_ = e.send(e.trace(msg, "delegate_exec", "delegating command to "+target, time.Now().UTC(), map[string]string{
			"command_id": commandID,
			"target":     target,
			"command":    command,
		}))

		select {
		case <-ctx.Done():
			return models.DelegateExecResult{}, ctx.Err()
		case result := <-ch:
			return delegateResultFrom(result), nil
		}
	}

	reply, err := e.responder.Reply(genCtx, msg, history, roomSummary, onEvent, requestPermission, delegateExec)
	if err != nil {
		e.handleReplyError(msg, err, startedAt)
		return
	}
	reply.Metadata["duration_ms"] = strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10)
	_ = e.send(reply)
	a.logger.Info("reply sent",
		slog.String("room_id", msg.RoomID),
		slog.String("to", msg.SenderID),
		slog.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
		slog.Int("reply_chars", len(reply.Content)),
		slog.Int("turn_budget_left", reply.TurnBudget),
	)
	doneMeta := map[string]string{"duration_ms": strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10)}
	_ = e.send(e.trace(msg, "done", "done", time.Now().UTC(), doneMeta))
}

// handleReplyError turns a failed generation into the right trace.
func (e *agentEngine) handleReplyError(msg models.ChatMessage, err error, startedAt time.Time) {
	a := e.app
	if errors.Is(err, models.ErrProviderInterrupted) {
		a.logger.Info("reply stopped",
			slog.String("room_id", msg.RoomID),
			slog.String("from", msg.SenderID),
			slog.Int64("elapsed_ms", time.Since(startedAt).Milliseconds()),
		)
		meta := map[string]string{"duration_ms": strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10)}
		_ = e.send(e.trace(msg, "stopped", "stopped", time.Now().UTC(), meta))
		return
	}
	a.logger.Error("agent reply failed", slog.Any("error", err))
	_ = e.send(e.trace(msg, "error", "reply failed", time.Now().UTC(), map[string]string{"error": err.Error()}))
}

// delegateResultFrom parses an executor command_result message into a
// DelegateExecResult.
func delegateResultFrom(msg models.ChatMessage) models.DelegateExecResult {
	res := models.DelegateExecResult{
		Output:    msg.Content,
		ErrorType: msg.Metadata["error_type"],
	}
	if v := strings.TrimSpace(msg.Metadata["exit_code"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			res.ExitCode = n
		}
	}
	res.TimedOut = msg.Metadata["timed_out"] == "true"
	res.StdoutTruncated = msg.Metadata["stdout_truncated"] == "true"
	res.StderrTruncated = msg.Metadata["stderr_truncated"] == "true"
	return res
}

// trace builds a trace ChatMessage for the given phase.
func (e *agentEngine) trace(src models.ChatMessage, phase, content string, at time.Time, extra map[string]string) models.ChatMessage {
	meta := map[string]string{
		"phase":    phase,
		"reply_to": src.ID,
		"provider": e.provider.Name(),
		"label":    e.label,
	}
	for k, v := range extra {
		if v != "" {
			meta[k] = v
		}
	}
	return models.ChatMessage{
		RoomID:     src.RoomID,
		Type:       models.MessageTypeTrace,
		SenderID:   e.app.cfg.AgentID,
		SenderKind: models.SenderKindAgent,
		TargetID:   src.SenderID,
		Content:    content,
		CreatedAt:  at,
		Metadata:   meta,
	}
}

// splitPatterns parses the pattern list an approver picked on the permission card.
func splitPatterns(raw string) []string {
	return models.DecodePatternList(raw)
}

// truncateForLog caps a string for structured-log fields.
func truncateForLog(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...(truncated)"
}
