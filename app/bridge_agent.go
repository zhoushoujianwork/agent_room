package app

import (
	"context"
	"errors"
	"log/slog"
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

// agentEngine owns the agent bridge's generation lifecycle independently of any
// single websocket connection. A reader goroutine (per connection) classifies
// inbound messages — dispatching stop signals and queuing reply jobs — while a
// single long-lived worker goroutine runs replies serially (one `claude -p` at
// a time). Crucially the worker, the in-flight generation handle, and the
// pending-permission registry all live here, NOT on the connection: when the
// websocket drops and reconnects, a running `claude -p` keeps going and its
// terminal trace (done/stopped/error) plus the final reply are buffered and
// flushed on reconnect. Before this split a reconnect orphaned the generation —
// the terminal trace was written to a dead socket and lost, the frontend clock
// climbed forever, and a later "stop" hit a fresh session that knew nothing of
// the old generation.
type agentEngine struct {
	app       *BridgeApp
	responder *agent.Responder
	chat      *chat.Service
	provider  models.AgentProvider
	label     string
	httpBase  string

	// engineCtx is the parent of every generation context. It is cancelled only
	// when the bridge process shuts down — never by a single connection ending —
	// so a disconnect does not abort an in-flight reply.
	engineCtx context.Context
	jobs      chan models.ChatMessage

	// sinkMu guards the swappable write sink and the outbound buffer together.
	// gorilla/websocket forbids concurrent writers, so all writes serialize here.
	sinkMu  sync.Mutex
	conn    *websocket.Conn
	pending []models.ChatMessage // buffered while disconnected; flushed on attach

	// genMu guards the in-flight generation handle below.
	genMu          sync.Mutex
	currentCancel  context.CancelFunc
	currentReplyTo string

	// permMu guards the pending-permission registry below. The reverse
	// approval channel is the structural mirror of the stop signal: generate()
	// blocks in a worker goroutine awaiting a decision while readLoop, in
	// another goroutine, delivers it by permission_id — same concurrency model
	// as stop cancelling genCtx.
	permMu      sync.Mutex
	pendingPerm map[string]chan models.PermissionDecision

	// execMu guards the pending-exec-delegation registry below. Structurally
	// identical to pendingPerm: the delegateExec closure (running in the worker
	// goroutine) blocks on a channel keyed by command_id while readLoop, in the
	// connection goroutine, delivers the matching command_result. This is how a
	// `claude -p` child gets a remote command's result without polling the relay
	// over HTTP — the bridge already sees the result on its own WebSocket.
	execMu      sync.Mutex
	pendingExec map[string]chan models.ChatMessage
}

// maxPendingOutbound caps the disconnect buffer. A single generation emits
// dozens of trace events (the worst stuck case we saw had 18), so this leaves
// ample headroom; on overflow we drop the OLDEST entry, which is always an
// intermediate trace — the reply and terminal trace are the newest and survive.
const maxPendingOutbound = 256

// send writes a message to the current connection, or buffers it when
// disconnected so a reconnect can flush it. A mid-write failure means the
// socket just broke: we drop the sink and buffer the message for next attach,
// so the reply/terminal trace produced right as a connection dies isn't lost.
func (e *agentEngine) send(msg models.ChatMessage) error {
	e.sinkMu.Lock()
	defer e.sinkMu.Unlock()
	if e.conn == nil {
		e.bufferLocked(msg)
		return nil
	}
	if err := e.conn.WriteJSON(msg); err != nil {
		e.conn = nil
		e.bufferLocked(msg)
		return nil
	}
	return nil
}

// bufferLocked appends to the outbound buffer, dropping the oldest entry when
// full. Caller must hold sinkMu.
func (e *agentEngine) bufferLocked(msg models.ChatMessage) {
	if len(e.pending) >= maxPendingOutbound {
		e.pending = e.pending[1:]
	}
	e.pending = append(e.pending, msg)
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
// even if generate() is momentarily not selecting yet. Caller must
// clearPermission(id) when done to avoid leaking the entry.
func (e *agentEngine) registerPermission(id string) <-chan models.PermissionDecision {
	ch := make(chan models.PermissionDecision, 1)
	e.permMu.Lock()
	if e.pendingPerm == nil {
		e.pendingPerm = make(map[string]chan models.PermissionDecision)
	}
	e.pendingPerm[id] = ch
	e.permMu.Unlock()
	return ch
}

// clearPermission removes a pending approval entry. Safe to call repeatedly.
func (e *agentEngine) clearPermission(id string) {
	e.permMu.Lock()
	delete(e.pendingPerm, id)
	e.permMu.Unlock()
}

// resolvePermission delivers a decision to a waiting RequestPermission closure.
// Returns true when a matching pending request existed and the decision was
// accepted; false when no request is waiting on that id (stale/duplicate).
func (e *agentEngine) resolvePermission(id string, decision models.PermissionDecision) bool {
	e.permMu.Lock()
	ch, ok := e.pendingPerm[id]
	if ok {
		delete(e.pendingPerm, id)
	}
	e.permMu.Unlock()
	if !ok {
		return false
	}
	// Channel is buffered (cap 1) and only resolved once, so this never blocks.
	ch <- decision
	return true
}

// registerExec records a pending exec-delegation channel keyed by command id
// (the id of the command message the bridge just sent) and returns the receive
// end. Buffered (cap 1) so resolveExec lands even if delegateExec is momentarily
// not selecting yet. Caller must clearExec(id) when done to avoid leaking.
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

// resolveExec delivers a command_result to a waiting delegateExec closure,
// matched by command_id. Returns true when a matching pending delegation
// existed; false when none is waiting (stale/duplicate/not ours).
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
	// Channel is buffered (cap 1) and only resolved once, so this never blocks.
	ch <- result
	return true
}

// start launches the single long-lived worker goroutine. The worker outlives
// individual connections, draining e.jobs under engineCtx, so a generation keeps
// running across a reconnect. Call once before the first serveConn.
func (e *agentEngine) start() {
	e.jobs = make(chan models.ChatMessage, 8)
	go e.worker()
}

// serveConn drives one websocket connection: it installs the conn as the engine's
// write sink (flushing any traces buffered while disconnected), announces
// presence, re-seeds history, then runs the read loop. On return — i.e. when the
// connection drops — it detaches the sink WITHOUT cancelling any in-flight
// generation, so the running reply survives to be flushed on the next connection.
func (e *agentEngine) serveConn(ctx context.Context, conn *websocket.Conn) error {
	a := e.app
	e.attach(conn)
	defer e.detach(conn)

	if err := e.send(models.ChatMessage{
		RoomID:     a.cfg.RoomID,
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

	// Seed local history from the relay so a freshly (re)connected bridge
	// remembers the room instead of starting blank. Best-effort.
	a.seedRoomHistory(ctx, e.chat, e.httpBase)

	return e.readLoop(ctx, conn)
}

// attach installs conn as the write sink and flushes any messages buffered while
// disconnected (terminal traces and the reply produced during the gap), in order.
func (e *agentEngine) attach(conn *websocket.Conn) {
	e.sinkMu.Lock()
	e.conn = conn
	pending := e.pending
	e.pending = nil
	e.sinkMu.Unlock()

	for _, msg := range pending {
		// Best-effort flush; if this conn also breaks, send() re-buffers.
		_ = e.send(msg)
	}
}

// detach clears the sink when its connection ends, but only if conn is still the
// active one (a racing reconnect may have already swapped in a newer conn). It
// does NOT touch the in-flight generation: the worker and its `claude -p` keep
// running, and their output buffers until the next attach.
func (e *agentEngine) detach(conn *websocket.Conn) {
	e.sinkMu.Lock()
	if e.conn == conn {
		e.conn = nil
	}
	e.sinkMu.Unlock()
}

// readLoop reads inbound messages from this connection's conn, stores them, and
// either dispatches a stop signal to the in-flight generation or queues a reply
// job on the engine. It returns when the connection closes or ctx is done. It
// reads from the conn passed in (not e.conn) so a racing reconnect swapping the
// write sink can't redirect this reader.
func (e *agentEngine) readLoop(ctx context.Context, conn *websocket.Conn) error {
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
		if _, err := e.chat.Add(ctx, msg.RoomID, msg); err != nil {
			a.logger.Warn("store incoming message failed", slog.Any("error", err))
		}

		// Control messages addressed to us carry out-of-band signals: stop
		// interrupts the in-flight reply; permission_reply feeds an approval
		// decision back to a blocked RequestPermission closure.
		if msg.Type == models.MessageTypeControl && msg.IsAddressedTo(a.cfg.AgentID) {
			switch strings.TrimSpace(msg.Metadata["operation"]) {
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

		// A command_result addressed to us carries the output of a command we
		// delegated to an executor peer. Route it by command_id to the blocked
		// delegateExec closure; this is the bridge-mediated alternative to the
		// agent polling the relay over HTTP. Non-delegated results (none pending
		// for that id) fall through harmlessly — they're already stored above and
		// ShouldReply ignores command_result anyway.
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

		// Non-blocking enqueue: the web composer locks while awaiting a reply,
		// so the queue should not back up. If it does, drop and warn rather
		// than stall the reader (which would also stall stop handling).
		select {
		case e.jobs <- msg:
		default:
			a.logger.Warn("reply queue full; dropping message",
				slog.String("room_id", msg.RoomID), slog.String("from", msg.SenderID))
		}
	}
}

// worker runs queued replies one at a time, for the lifetime of the engine
// (across reconnects). Each generation gets its own cancelable context
// registered as the in-flight handle so a stop signal can interrupt it.
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
	// e.send never errors (it buffers while disconnected), so generation always
	// proceeds even if no connection is currently attached.
	_ = e.send(e.trace(msg, "thinking", "thinking", startedAt, nil))

	onEvent := func(ev models.ProviderEvent) {
		// Permission requests are surfaced by the requestPermission closure
		// below, which owns the permission_id <-> pending-channel mapping. The
		// generic trace path skips them so a provider that also emits this
		// event can't produce a duplicate, id-less approval card.
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

	// requestPermission is the reverse channel mirror of onEvent: it surfaces a
	// permission_request trace to the room, then blocks the in-flight
	// generation until readLoop delivers a decision (control/permission_reply)
	// or genCtx is cancelled (e.g. a stop signal). nil-RequestPermission
	// providers never call this and keep the old auto-allow behavior.
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
		// Log the ask with tool/input so the later "delivered" line (which only
		// has the permission_id) can be correlated back to what was approved.
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
			// Context cancelled (stop or connection teardown) while awaiting
			// approval: surface as an interruption so the provider can abort.
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

	// delegateExec lets the provider hand a shell command to an executor peer and
	// block for its result, replacing the agent's HTTP poll loop. We mint the
	// command id up front so we can register the pending channel BEFORE sending,
	// closing the race where the result could arrive before we start waiting. The
	// command goes out over the bridge's own WS; the relay stamps the recipient's
	// exec_token onto it (relay-mediated), so we never handle the token here.
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
		// Surface a room-visible trace so the timeline shows the agent dispatching
		// a remote command, mirroring the permission_request card.
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
	// e.send buffers if disconnected, so the reply and its done trace survive a
	// reconnect rather than vanishing into a dead socket.
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

// handleReplyError turns a failed generation into the right trace: a
// deliberate stop becomes phase="stopped" (not an error), anything else
// becomes phase="error" carrying the message.
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

// delegateResultFrom parses an executor's command_result message into a
// DelegateExecResult. The executor encodes exit_code/timed_out/truncation flags
// as string metadata (see executor.baseMetadata) and renders the combined
// stdout+stderr into the message Content (formatResult). Missing/garbage fields
// degrade to zero values rather than erroring — the agent still gets a usable
// (if partial) result.
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

// trace builds a trace ChatMessage for the given phase, carrying the common
// reply_to/provider/label metadata plus any phase-specific extras.
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

// splitPatterns parses the pattern list an approver picked on the permission
// card (control metadata.patterns): JSON array first (multi-line patterns),
// newline-joined fallback. See models.EncodePatternList.
func splitPatterns(raw string) []string {
	return models.DecodePatternList(raw)
}

// truncateForLog caps a string for structured-log fields so a huge tool input
// can't flood the bridge log.
func truncateForLog(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...(truncated)"
}
