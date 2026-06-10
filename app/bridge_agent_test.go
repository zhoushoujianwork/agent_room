package app

import (
	"strconv"
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
// stream: with no connection attached, send() must buffer (not drop) so a
// terminal trace produced during a disconnect survives to the next attach.
func TestSendBuffersWhileDisconnected(t *testing.T) {
	e := &agentEngine{}

	for i := 0; i < 3; i++ {
		if err := e.send(models.ChatMessage{Type: models.MessageTypeTrace, Content: "t"}); err != nil {
			t.Fatalf("send while disconnected returned error: %v", err)
		}
	}
	if len(e.pending) != 3 {
		t.Fatalf("pending = %d, want 3 buffered messages", len(e.pending))
	}
}

// TestBufferLockedDropsOldest verifies the disconnect buffer is bounded and
// evicts the OLDEST entry on overflow, so the newest (reply + terminal trace)
// always survive.
func TestBufferLockedDropsOldest(t *testing.T) {
	e := &agentEngine{}
	for i := 0; i < maxPendingOutbound+10; i++ {
		e.send(models.ChatMessage{Content: string(rune('a' + i%26)), Metadata: map[string]string{"seq": itoa(i)}})
	}
	if len(e.pending) != maxPendingOutbound {
		t.Fatalf("pending = %d, want cap %d", len(e.pending), maxPendingOutbound)
	}
	// The very last message sent must still be present as the tail.
	last := e.pending[len(e.pending)-1]
	if last.Metadata["seq"] != itoa(maxPendingOutbound+9) {
		t.Fatalf("tail seq = %q, want newest %q", last.Metadata["seq"], itoa(maxPendingOutbound+9))
	}
}

// TestDetachKeepsGenerationState verifies a connection ending does NOT clear the
// in-flight handle, so a stop arriving on a later connection still cancels the
// generation that started under an earlier one.
func TestDetachKeepsGenerationState(t *testing.T) {
	e := &agentEngine{}
	cancelled := false
	e.registerGeneration("msg_1", func() { cancelled = true })

	e.detach(nil) // simulate the old connection going away

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
