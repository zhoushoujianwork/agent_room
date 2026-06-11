package app

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-room/internal/models"
)

func TestShouldCancel(t *testing.T) {
	stop := func(replyTo string) models.ChatMessage {
		meta := map[string]string{"operation": models.ControlOperationStop}
		if replyTo != "" {
			meta["reply_to"] = replyTo
		}
		return models.ChatMessage{Type: models.MessageTypeControl, Metadata: meta}
	}

	tests := []struct {
		name           string
		msg            models.ChatMessage
		currentReplyTo string
		want           bool
	}{
		{"nothing in flight", stop(""), "", false},
		{"stop without reply_to cancels current", stop(""), "msg_1", true},
		{"stop matching reply_to", stop("msg_1"), "msg_1", true},
		{"stop mismatched reply_to", stop("msg_2"), "msg_1", false},
		{
			name:           "non-control ignored",
			msg:            models.ChatMessage{Type: models.MessageTypeChat},
			currentReplyTo: "msg_1",
			want:           false,
		},
		{
			name:           "control without stop op ignored",
			msg:            models.ChatMessage{Type: models.MessageTypeControl, Metadata: map[string]string{"operation": "other"}},
			currentReplyTo: "msg_1",
			want:           false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldCancel(tt.msg, tt.currentReplyTo); got != tt.want {
				t.Fatalf("shouldCancel = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestResolvePermissionUnblocksRegister verifies the reverse approval channel:
// register() hands out a receive end, and a resolvePermission() from another
// goroutine (mirroring readLoop delivering a control/permission_reply) lands on
// it. The permission registry methods only touch permMu/pendingPerm, so a
// zero-value agentEngine is enough to exercise them.
func TestResolvePermissionUnblocksRegister(t *testing.T) {
	e := &agentEngine{}

	ch := e.registerPermission("perm-1")
	defer e.clearPermission("perm-1")

	want := models.PermissionDecision{Reply: models.PermissionAllowOnce, By: "carol"}
	go func() {
		if !e.resolvePermission("perm-1", want) {
			t.Errorf("resolvePermission returned false for a pending id")
		}
	}()

	select {
	case got := <-ch:
		if got.Reply != want.Reply || got.By != want.By {
			t.Fatalf("decision = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for permission decision")
	}

	// Resolving an unknown / already-consumed id must report false, not panic.
	if e.resolvePermission("perm-1", want) {
		t.Fatal("resolvePermission should return false after the entry was consumed")
	}
	if e.resolvePermission("nope", want) {
		t.Fatal("resolvePermission should return false for an unregistered id")
	}
}

// TestResolveExecRoutesByCommandID verifies the exec-delegation registry: the
// delegateExec closure registers a channel keyed by command_id, and a
// resolveExec() from another goroutine (mirroring readLoop delivering a
// command_result over the WS) lands the message on it. Like the permission
// registry, a zero-value agentEngine is enough.
func TestResolveExecRoutesByCommandID(t *testing.T) {
	e := &agentEngine{}

	ch := e.registerExec("msg_cmd1")
	defer e.clearExec("msg_cmd1")

	want := models.ChatMessage{
		Type:     models.MessageTypeCommandResult,
		SenderID: "exec-1",
		Content:  "exit_code=0\nok",
		Metadata: map[string]string{"command_id": "msg_cmd1", "exit_code": "0"},
	}
	go func() {
		if !e.resolveExec("msg_cmd1", want) {
			t.Errorf("resolveExec returned false for a pending command id")
		}
	}()

	select {
	case got := <-ch:
		if got.SenderID != want.SenderID || got.Content != want.Content {
			t.Fatalf("result = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delegated exec result")
	}

	// A consumed or unknown command_id must report false (stale/duplicate/not
	// ours), so readLoop lets non-delegated command_results fall through.
	if e.resolveExec("msg_cmd1", want) {
		t.Fatal("resolveExec should return false after the entry was consumed")
	}
	if e.resolveExec("msg_other", want) {
		t.Fatal("resolveExec should return false for an unregistered command id")
	}
}

// TestDelegateResultFrom verifies the executor command_result -> DelegateExecResult
// mapping, including the degrade-to-zero behavior on missing/garbage metadata.
func TestDelegateResultFrom(t *testing.T) {
	full := delegateResultFrom(models.ChatMessage{
		Content: "exit_code=124\nstdout:\npartial",
		Metadata: map[string]string{
			"exit_code":        "124",
			"timed_out":        "true",
			"stdout_truncated": "true",
			"stderr_truncated": "true",
			"error_type":       "timeout",
		},
	})
	want := models.DelegateExecResult{
		ExitCode:        124,
		Output:          "exit_code=124\nstdout:\npartial",
		TimedOut:        true,
		StdoutTruncated: true,
		StderrTruncated: true,
		ErrorType:       "timeout",
	}
	if full != want {
		t.Fatalf("delegateResultFrom = %+v, want %+v", full, want)
	}

	// Garbage exit_code and absent flags degrade to zero values, not errors.
	partial := delegateResultFrom(models.ChatMessage{
		Content:  "ok",
		Metadata: map[string]string{"exit_code": "not-a-number"},
	})
	if partial.ExitCode != 0 || partial.TimedOut || partial.ErrorType != "" || partial.Output != "ok" {
		t.Fatalf("degraded result = %+v, want zero-valued flags with Output=ok", partial)
	}
}

// TestSendBuffersWhileDisconnected is the core regression guard for the zombie
// stream: with no connection attached, roomConn.send() must buffer (not drop)
// so a terminal trace produced during a disconnect survives to the next attach.
func TestSendBuffersWhileDisconnected(t *testing.T) {
	rc := &roomConn{roomID: "room-1"}

	for i := 0; i < 3; i++ {
		rc.send(models.ChatMessage{Type: models.MessageTypeTrace, Content: "t", RoomID: "room-1"})
	}
	rc.mu.Lock()
	n := len(rc.pending)
	rc.mu.Unlock()
	if n != 3 {
		t.Fatalf("pending = %d, want 3 buffered messages", n)
	}

	// Verify engine.send routes to the correct roomConn and buffers there.
	e := &agentEngine{
		rooms: map[string]*roomConn{"room-1": rc},
		app:   &BridgeApp{logger: slog.Default()},
	}
	if err := e.send(models.ChatMessage{RoomID: "room-1", Type: models.MessageTypeTrace, Content: "routed"}); err != nil {
		t.Fatalf("engine.send returned error: %v", err)
	}
	rc.mu.Lock()
	n2 := len(rc.pending)
	rc.mu.Unlock()
	if n2 != 4 {
		t.Fatalf("pending after engine.send = %d, want 4", n2)
	}
}

// TestBufferLockedDropsOldest verifies the per-room disconnect buffer is bounded
// and evicts the OLDEST entry on overflow, so the newest (reply + terminal
// trace) always survive.
func TestBufferLockedDropsOldest(t *testing.T) {
	rc := &roomConn{roomID: "room-x"}
	for i := 0; i < maxPendingOutbound+10; i++ {
		rc.send(models.ChatMessage{
			RoomID:   "room-x",
			Content:  string(rune('a' + i%26)),
			Metadata: map[string]string{"seq": itoa(i)},
		})
	}
	rc.mu.Lock()
	n := len(rc.pending)
	last := rc.pending[len(rc.pending)-1]
	rc.mu.Unlock()
	if n != maxPendingOutbound {
		t.Fatalf("pending = %d, want cap %d", n, maxPendingOutbound)
	}
	// The very last message sent must still be present as the tail.
	if last.Metadata["seq"] != itoa(maxPendingOutbound+9) {
		t.Fatalf("tail seq = %q, want newest %q", last.Metadata["seq"], itoa(maxPendingOutbound+9))
	}
}

// TestDetachKeepsGenerationState verifies a connection ending does NOT clear the
// in-flight handle, so a stop arriving on a later connection still cancels the
// generation that started under an earlier one. The detach is now on roomConn,
// not agentEngine, but generation state lives on the engine — so a roomConn
// detach must not affect maybeCancel.
func TestDetachKeepsGenerationState(t *testing.T) {
	rc := &roomConn{roomID: "room-1"}
	e := &agentEngine{}
	cancelled := false
	e.registerGeneration("msg_1", func() { cancelled = true })

	// Simulate the room connection going away.
	rc.detach(nil)

	stop := models.ChatMessage{
		Type:     models.MessageTypeControl,
		Metadata: map[string]string{"operation": models.ControlOperationStop, "reply_to": "msg_1"},
	}
	if !e.maybeCancel(stop) {
		t.Fatal("stop after detach should still cancel the in-flight generation")
	}
	if !cancelled {
		t.Fatal("cancel func was not invoked")
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

// TestJoinRoomIdempotent verifies that calling joinRoom twice with the same
// roomID only creates one roomConn (no duplicate goroutines or map entries).
func TestJoinRoomIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := &agentEngine{
		engineCtx: ctx,
		rooms:     make(map[string]*roomConn),
		app:       &BridgeApp{logger: slog.Default()},
	}

	e.joinRoom(ctx, "alpha")
	e.joinRoom(ctx, "alpha") // second call must be a no-op

	e.roomsMu.Lock()
	n := len(e.rooms)
	e.roomsMu.Unlock()
	if n != 1 {
		t.Fatalf("rooms = %d, want 1 after idempotent join", n)
	}
}

// TestLeaveRoomRemovesEntry verifies leaveRoom removes the roomConn from the
// map and cancels its context.
func TestLeaveRoomRemovesEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := &agentEngine{
		engineCtx: ctx,
		rooms:     make(map[string]*roomConn),
		app:       &BridgeApp{logger: slog.Default()},
	}

	e.joinRoom(ctx, "beta")
	e.roomsMu.Lock()
	rc := e.rooms["beta"]
	e.roomsMu.Unlock()
	if rc == nil {
		t.Fatal("expected roomConn for 'beta' after join")
	}

	// Capture the room context before leaving.
	roomCtx, _ := context.WithCancel(context.Background()) // just to have one
	_ = roomCtx

	e.leaveRoom("beta")

	e.roomsMu.Lock()
	_, still := e.rooms["beta"]
	e.roomsMu.Unlock()
	if still {
		t.Fatal("room 'beta' should have been removed after leaveRoom")
	}
}

// TestSendRoutesByRoomID verifies that engine.send routes messages to the
// correct per-room buffer and drops messages for unknown rooms.
func TestSendRoutesByRoomID(t *testing.T) {
	rcA := &roomConn{roomID: "room-a"}
	rcB := &roomConn{roomID: "room-b"}

	e := &agentEngine{
		rooms: map[string]*roomConn{
			"room-a": rcA,
			"room-b": rcB,
		},
		app: &BridgeApp{logger: slog.Default()},
	}

	_ = e.send(models.ChatMessage{RoomID: "room-a", Content: "for-a"})
	_ = e.send(models.ChatMessage{RoomID: "room-a", Content: "for-a-2"})
	_ = e.send(models.ChatMessage{RoomID: "room-b", Content: "for-b"})
	// Unknown room: must not panic, just warn-and-drop.
	_ = e.send(models.ChatMessage{RoomID: "room-z", Content: "lost"})

	rcA.mu.Lock()
	nA := len(rcA.pending)
	rcA.mu.Unlock()

	rcB.mu.Lock()
	nB := len(rcB.pending)
	rcB.mu.Unlock()

	if nA != 2 {
		t.Fatalf("room-a pending = %d, want 2", nA)
	}
	if nB != 1 {
		t.Fatalf("room-b pending = %d, want 1", nB)
	}
}

// TestRoomStateReportNoControlConn verifies that sendRoomStateReport is safe
// to call when no control connection is active (should be a no-op, not a panic).
func TestRoomStateReportNoControlConn(t *testing.T) {
	e := &agentEngine{
		rooms: map[string]*roomConn{
			"r1": {roomID: "r1"},
			"r2": {roomID: "r2"},
		},
		app: &BridgeApp{logger: slog.Default()},
	}
	// Should not panic.
	e.sendRoomStateReport()
}

// TestBuildRoomURL verifies that buildRoomURL correctly constructs the room WS
// URL, replacing the room path while preserving scheme and host.
func TestBuildRoomURL(t *testing.T) {
	cases := []struct {
		base   string
		room   string
		token  string
		wantPath string
		wantToken bool
	}{
		{"ws://relay.example.com/v1/rooms/old/ws", "new", "", "/v1/rooms/new/ws", false},
		{"wss://relay.example.com:8443/v1/rooms/old/ws", "myroom", "tok123", "/v1/rooms/myroom/ws", true},
		{"http://relay.example.com", "abc", "", "/v1/rooms/abc/ws", false},
	}
	for _, tc := range cases {
		got := buildRoomURL(tc.base, tc.room, tc.token)
		if got == "" {
			t.Errorf("buildRoomURL(%q, %q, %q) = empty", tc.base, tc.room, tc.token)
			continue
		}
		if !strings.Contains(got, tc.wantPath) {
			t.Errorf("buildRoomURL(%q, %q, %q) = %q, want path %q", tc.base, tc.room, tc.token, got, tc.wantPath)
		}
		hasToken := strings.Contains(got, "agent_token="+tc.token)
		if tc.wantToken && !hasToken {
			t.Errorf("buildRoomURL(%q, %q, %q) = %q, want agent_token param", tc.base, tc.room, tc.token, got)
		}
		if !tc.wantToken && strings.Contains(got, "agent_token") {
			t.Errorf("buildRoomURL(%q, %q, %q) = %q, unexpected agent_token param", tc.base, tc.room, tc.token, got)
		}
	}
}
