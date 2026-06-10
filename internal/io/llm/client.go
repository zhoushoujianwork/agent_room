// Package llm is a minimal HTTP client for the company LLM endpoint, which
// is Anthropic Messages API compatible (POST {base}/v1/messages with an
// x-api-key header). The relay uses it only to summarize room history; it is
// intentionally tiny and has no streaming, tools, or retries.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrTruncated reports that the model stopped at max_tokens, so the returned
// text is cut off mid-thought. Complete still returns the partial text
// alongside this error; callers decide whether to salvage or discard it.
var ErrTruncated = errors.New("llm response truncated at max_tokens")

type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// New returns a client for the given Anthropic-compatible endpoint. baseURL
// is the origin (e.g. https://llm.example.com); "/v1/messages" is
// appended per request.
func New(apiKey, baseURL, model string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: 90 * time.Second},
	}
}

type messagesRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	System    string           `json:"system,omitempty"`
	Messages  []requestMessage `json:"messages"`
}

type requestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
}

// Complete sends a single-turn prompt and returns the concatenated text of
// the response. system may be empty.
func (c *Client) Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	body, err := json.Marshal(messagesRequest{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []requestMessage{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm status %d", resp.StatusCode)
	}

	var parsed messagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("llm decode: %w", err)
	}
	var sb strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return "", fmt.Errorf("llm returned empty content")
	}
	if parsed.StopReason == "max_tokens" {
		return out, ErrTruncated
	}
	return out, nil
}
