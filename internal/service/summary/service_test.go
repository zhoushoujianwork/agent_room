package summary

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"agent-room/internal/io/llm"
	"agent-room/internal/io/memory"
	"agent-room/internal/models"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeCompleter struct {
	mu     sync.Mutex
	calls  int
	prompt string
	out    string
	err    error
}

func (f *fakeCompleter) Complete(_ context.Context, _, prompt string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.prompt = prompt
	if f.out == "" && f.err == nil {
		return "digest of room", nil
	}
	return f.out, f.err
}

func TestRefreshWritesSummary(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	for _, c := range []string{"看下出口IP", "好的我查一下", "结果是1.2.3.4"} {
		_ = store.Append(ctx, models.ChatMessage{ID: c, RoomID: "r", Type: models.MessageTypeChat, Content: c})
	}
	fc := &fakeCompleter{}
	svc := New(store, fc, 20, time.Minute, testLogger())

	if err := svc.Refresh(ctx, "r"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got, _ := store.GetSummary(ctx, "r")
	if got.Summary != "digest of room" {
		t.Fatalf("summary = %q", got.Summary)
	}
	if got.CoveredSeq != 3 {
		t.Fatalf("coveredSeq = %d want 3", got.CoveredSeq)
	}
	if fc.calls != 1 {
		t.Fatalf("llm calls = %d want 1", fc.calls)
	}
}

func TestRefreshTruncatedOutputTrimsDanglingLine(t *testing.T) {
	store := memory.NewStore()
	ctx := context.Background()
	_ = store.Append(ctx, models.ChatMessage{ID: "1", RoomID: "r", Type: models.MessageTypeChat, Content: "聊点啥"})
	fc := &fakeCompleter{out: "- 第一条完整结论\n- 第二条完整结论\n- 第三条说到一半就被截", err: llm.ErrTruncated}
	svc := New(store, fc, 20, time.Minute, testLogger())

	if err := svc.Refresh(ctx, "r"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got, _ := store.GetSummary(ctx, "r")
	want := "- 第一条完整结论\n- 第二条完整结论"
	if got.Summary != want {
		t.Fatalf("summary = %q want %q", got.Summary, want)
	}
}

func TestTrimDanglingLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\nb\nc", "a\nb"},
		{"a\nb\n", "a"},
		{"single line no newline", "single line no newline"},
		{"a\n", "a\n"},
	}
	for _, c := range cases {
		if got := trimDanglingLine(c.in); got != c.want {
			t.Errorf("trimDanglingLine(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestNewReturnsNilWhenNoLLM(t *testing.T) {
	if svc := New(memory.NewStore(), nil, 20, time.Minute, testLogger()); svc != nil {
		t.Fatal("expected nil service when llm is nil")
	}
	// Nil service methods are safe no-ops.
	var nilSvc *Service
	nilSvc.Notify("r", models.MessageTypeChat)
	if err := nilSvc.Refresh(context.Background(), "r"); err != nil {
		t.Fatalf("nil refresh: %v", err)
	}
}

func TestRefreshEmptyRoomNoCall(t *testing.T) {
	store := memory.NewStore()
	fc := &fakeCompleter{}
	svc := New(store, fc, 20, time.Minute, testLogger())
	if err := svc.Refresh(context.Background(), "empty"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if fc.calls != 0 {
		t.Fatalf("llm called %d times on empty room", fc.calls)
	}
}
