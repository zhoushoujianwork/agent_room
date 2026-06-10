package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompleteSendsAnthropicShape(t *testing.T) {
	var gotKey, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"pong"},{"type":"text","text":" tail"}]}`))
	}))
	defer srv.Close()

	c := New("secret-key", srv.URL, "test-model")
	out, err := c.Complete(context.Background(), "be terse", "ping", 64)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if out != "pong tail" {
		t.Fatalf("text = %q want concatenated", out)
	}
	if gotKey != "secret-key" {
		t.Fatalf("x-api-key = %q", gotKey)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["model"] != "test-model" || gotBody["system"] != "be terse" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestCompleteNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := New("k", srv.URL, "m").Complete(context.Background(), "", "x", 10); err == nil {
		t.Fatal("expected error on 401")
	}
}
