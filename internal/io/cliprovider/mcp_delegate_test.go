package cliprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"agent-room/internal/models"
)

// postMCP 模拟 claude 作为 MCP client: 带 token 把一条 JSON-RPC 消息 POST 给本地
// 端点, 返回原始 HTTP 响应(调用方自行解码/断言状态码)。
func postMCP(t *testing.T, b *execDelegateBroker, token, payload string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.endpoint, bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set(mcpTokenHeader, token)
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post mcp: %v", err)
	}
	return resp
}

// rpcCall 发送一条带 id 的 JSON-RPC 请求并解码响应。
func rpcCall(t *testing.T, b *execDelegateBroker, payload string) jsonRPCResponse {
	t.Helper()
	resp := postMCP(t, b, b.token, payload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, string(body))
	}
	var out jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode rpc response: %v", err)
	}
	if out.JSONRPC != "2.0" {
		t.Fatalf("jsonrpc = %q, want 2.0", out.JSONRPC)
	}
	return out
}

// resultAs 把 jsonRPCResponse.Result(handler 里是 map[string]any)经 JSON 往返
// 解到目标结构, 便于断言嵌套字段。
func resultAs(t *testing.T, res jsonRPCResponse, target any) {
	t.Helper()
	if res.Error != nil {
		t.Fatalf("rpc error: %d %s", res.Error.Code, res.Error.Message)
	}
	buf, err := json.Marshal(res.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(buf, target); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
}

// toolResult 是 tools/call 返回的 MCP tool result 契约形态。
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// callExecRemote 发一次 exec_remote tools/call 并解出 tool result。
func callExecRemote(t *testing.T, b *execDelegateBroker, args string) toolResult {
	t.Helper()
	res := rpcCall(t, b, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"exec_remote","arguments":`+args+`}}`)
	var out toolResult
	resultAs(t, res, &out)
	if len(out.Content) != 1 || out.Content[0].Type != "text" {
		t.Fatalf("tool result content = %+v, want single text entry", out.Content)
	}
	return out
}

func newTestBroker(t *testing.T, delegate func(context.Context, models.DelegateExecRequest) (models.DelegateExecResult, error)) *execDelegateBroker {
	t.Helper()
	b, err := newExecDelegateBroker(context.Background(), models.ProviderRequest{DelegateExec: delegate})
	if err != nil {
		t.Fatalf("newExecDelegateBroker: %v", err)
	}
	t.Cleanup(b.close)
	return b
}

// TestExecDelegateBrokerInitialize: initialize 握手回报协议版本与 tools 能力,
// 且生成的 --mcp-config 指向端点并携带 token header。
func TestExecDelegateBrokerInitialize(t *testing.T) {
	b := newTestBroker(t, nil)

	res := rpcCall(t, b, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude","version":"1.0"}}}`)
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	resultAs(t, res, &init)
	if init.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("protocolVersion = %q, want %q", init.ProtocolVersion, mcpProtocolVersion)
	}
	if init.Capabilities.Tools == nil {
		t.Fatal("capabilities.tools missing")
	}
	if init.ServerInfo.Name != mcpServerName {
		t.Fatalf("serverInfo.name = %q, want %q", init.ServerInfo.Name, mcpServerName)
	}

	// --mcp-config 文件就位且内容指向本端点、带 token header。
	buf, err := os.ReadFile(b.configPath)
	if err != nil {
		t.Fatalf("read mcp config: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(buf, &cfg); err != nil {
		t.Fatalf("unmarshal mcp config: %v", err)
	}
	srv, ok := cfg.MCPServers[mcpServerName]
	if !ok {
		t.Fatalf("mcp config missing server %q: %s", mcpServerName, buf)
	}
	if srv.Type != "http" || srv.URL != b.endpoint {
		t.Fatalf("server = %+v, want type=http url=%s", srv, b.endpoint)
	}
	if srv.Headers[mcpTokenHeader] != b.token {
		t.Fatalf("config token header = %q, want broker token", srv.Headers[mcpTokenHeader])
	}
	if !strings.HasPrefix(b.endpoint, "http://127.0.0.1:") {
		t.Fatalf("endpoint = %q, want 127.0.0.1 only", b.endpoint)
	}
}

// TestExecDelegateBrokerToolsList: tools/list 暴露唯一的 exec_remote 工具,
// schema 必填 target+command。
func TestExecDelegateBrokerToolsList(t *testing.T) {
	b := newTestBroker(t, nil)

	res := rpcCall(t, b, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var list struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Type       string         `json:"type"`
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	resultAs(t, res, &list)
	if len(list.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(list.Tools))
	}
	tool := list.Tools[0]
	if tool.Name != execDelegateToolName {
		t.Fatalf("tool name = %q, want %q", tool.Name, execDelegateToolName)
	}
	if tool.InputSchema.Type != "object" {
		t.Fatalf("schema type = %q, want object", tool.InputSchema.Type)
	}
	for _, key := range []string{"target", "command", "timeout_ms", "cwd", "shell"} {
		if _, ok := tool.InputSchema.Properties[key]; !ok {
			t.Fatalf("schema missing property %q", key)
		}
	}
	if len(tool.InputSchema.Required) != 2 || tool.InputSchema.Required[0] != "target" || tool.InputSchema.Required[1] != "command" {
		t.Fatalf("required = %v, want [target command]", tool.InputSchema.Required)
	}
}

// TestExecDelegateBrokerToolsCallMapsArguments: tools/call 的参数被完整映射进
// DelegateExecRequest, 成功结果以 isError=false 的文本返回。
func TestExecDelegateBrokerToolsCallMapsArguments(t *testing.T) {
	var got models.DelegateExecRequest
	var calls int
	b := newTestBroker(t, func(ctx context.Context, r models.DelegateExecRequest) (models.DelegateExecResult, error) {
		calls++
		got = r
		return models.DelegateExecResult{ExitCode: 0, Output: "exit_code=0\nhello"}, nil
	})

	out := callExecRemote(t, b, `{"target":"exec-1","command":"echo hello","timeout_ms":5000,"cwd":"/tmp","shell":"bash"}`)
	if out.IsError {
		t.Fatalf("isError = true on success, content: %+v", out.Content)
	}
	if calls != 1 {
		t.Fatalf("DelegateExec called %d times, want 1", calls)
	}
	want := models.DelegateExecRequest{TargetID: "exec-1", Command: "echo hello", TimeoutMS: 5000, Cwd: "/tmp", Shell: "bash"}
	if got != want {
		t.Fatalf("DelegateExecRequest = %+v, want %+v", got, want)
	}
	if !strings.Contains(out.Content[0].Text, "hello") {
		t.Fatalf("result text = %q, want executor output", out.Content[0].Text)
	}
}

// TestExecDelegateBrokerToolsCallResultFlags: 非零退出码 / timed_out / error_type
// 标记为 isError=true 并补注进文本。
func TestExecDelegateBrokerToolsCallResultFlags(t *testing.T) {
	b := newTestBroker(t, func(ctx context.Context, r models.DelegateExecRequest) (models.DelegateExecResult, error) {
		return models.DelegateExecResult{ExitCode: 124, Output: "exit_code=124", TimedOut: true, ErrorType: "timeout"}, nil
	})

	out := callExecRemote(t, b, `{"target":"exec-1","command":"sleep 999"}`)
	if !out.IsError {
		t.Fatal("isError = false, want true for non-zero exit")
	}
	text := out.Content[0].Text
	if !strings.Contains(text, "timed_out=true") || !strings.Contains(text, "error_type=timeout") {
		t.Fatalf("result text missing flags: %q", text)
	}
}

// TestExecDelegateBrokerToolsCallDelegateError: DelegateExec 返回错误(stop 中断/
// 发送失败)时以 isError=true 的 tool result 呈现, 而非协议级 error。
func TestExecDelegateBrokerToolsCallDelegateError(t *testing.T) {
	b := newTestBroker(t, func(ctx context.Context, r models.DelegateExecRequest) (models.DelegateExecResult, error) {
		return models.DelegateExecResult{}, errors.New("context canceled")
	})

	out := callExecRemote(t, b, `{"target":"exec-1","command":"ls"}`)
	if !out.IsError {
		t.Fatal("isError = false, want true on delegate error")
	}
	if !strings.Contains(out.Content[0].Text, "context canceled") {
		t.Fatalf("result text = %q, want delegate error surfaced", out.Content[0].Text)
	}
}

// TestExecDelegateBrokerToolsCallMissingArgs: target/command 缺失直接拒绝,
// 不触达 DelegateExec。
func TestExecDelegateBrokerToolsCallMissingArgs(t *testing.T) {
	var calls int
	b := newTestBroker(t, func(ctx context.Context, r models.DelegateExecRequest) (models.DelegateExecResult, error) {
		calls++
		return models.DelegateExecResult{}, nil
	})

	for _, args := range []string{`{"target":"exec-1"}`, `{"command":"ls"}`, `{"target":"  ","command":"ls"}`} {
		out := callExecRemote(t, b, args)
		if !out.IsError {
			t.Fatalf("args %s: isError = false, want true", args)
		}
	}
	if calls != 0 {
		t.Fatalf("DelegateExec called %d times on missing args, want 0", calls)
	}
}

// TestExecDelegateBrokerUnknownToolAndMethod: 未知工具/方法返回协议级 error。
func TestExecDelegateBrokerUnknownToolAndMethod(t *testing.T) {
	b := newTestBroker(t, nil)

	res := rpcCall(t, b, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	if res.Error == nil || res.Error.Code != -32602 {
		t.Fatalf("unknown tool error = %+v, want -32602", res.Error)
	}

	res = rpcCall(t, b, `{"jsonrpc":"2.0","id":5,"method":"resources/list"}`)
	if res.Error == nil || res.Error.Code != -32601 {
		t.Fatalf("unknown method error = %+v, want -32601", res.Error)
	}
}

// TestExecDelegateBrokerNotificationAccepted: 无 id 的通知(initialized)返回 202,
// 无响应体。
func TestExecDelegateBrokerNotificationAccepted(t *testing.T) {
	b := newTestBroker(t, nil)

	resp := postMCP(t, b, b.token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
}

// TestExecDelegateBrokerRejectsBadToken: 缺失/错误 token 一律 403, 防同机伪造,
// 且不触达 DelegateExec。
func TestExecDelegateBrokerRejectsBadToken(t *testing.T) {
	var calls int
	b := newTestBroker(t, func(ctx context.Context, r models.DelegateExecRequest) (models.DelegateExecResult, error) {
		calls++
		return models.DelegateExecResult{}, nil
	})

	for _, token := range []string{"", "wrong-token"} {
		resp := postMCP(t, b, token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"exec_remote","arguments":{"target":"x","command":"ls"}}}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("token %q: status = %d, want 403", token, resp.StatusCode)
		}
	}
	if calls != 0 {
		t.Fatalf("DelegateExec called %d times on bad token, want 0", calls)
	}
}

// TestExecDelegateBrokerContextCancelUnblocks: 等待结果期间 ctx 取消(等价 stop)
// 必须让阻塞的 tools/call 返回, 端点不悬挂。
func TestExecDelegateBrokerContextCancelUnblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b, err := newExecDelegateBroker(ctx, models.ProviderRequest{
		DelegateExec: func(reqCtx context.Context, r models.DelegateExecRequest) (models.DelegateExecResult, error) {
			// 模拟 bridge 闭包行为: 阻塞直到 ctx 取消。
			<-reqCtx.Done()
			return models.DelegateExecResult{}, reqCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("newExecDelegateBroker: %v", err)
	}
	t.Cleanup(b.close)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan toolResult, 1)
	go func() {
		done <- callExecRemote(t, b, `{"target":"exec-1","command":"sleep 999"}`)
	}()

	select {
	case out := <-done:
		if !out.IsError {
			t.Fatal("isError = false, want true after ctx cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tools/call did not unblock after ctx cancel")
	}
}

// TestExecDelegateBrokerCloseCleansUp: close 后临时目录(含 --mcp-config)被清理。
func TestExecDelegateBrokerCloseCleansUp(t *testing.T) {
	b, err := newExecDelegateBroker(context.Background(), models.ProviderRequest{})
	if err != nil {
		t.Fatalf("newExecDelegateBroker: %v", err)
	}
	dir := b.tmpDir
	b.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("tmp dir %q not cleaned up: err=%v", dir, err)
	}
}

// TestExecutorUsageGuideMCPMode: delegateViaMCP=true 时指引换成 exec_remote 工具
// (不教 curl), 且不依赖 RelayHTTPURL; false 时退回 curl 指引(需要 RelayHTTPURL)。
func TestExecutorUsageGuideMCPMode(t *testing.T) {
	history := []models.ChatMessage{
		{
			Type:     models.MessageTypePresence,
			SenderID: "exec-9",
			Metadata: map[string]string{"mode": "executor", "os": "windows"},
		},
	}
	req := models.ProviderRequest{
		RoomID:  "room-1",
		AgentID: "agent-1",
		History: history,
		Input:   models.ChatMessage{RoomID: "room-1"},
	}

	// MCP 模式: 没有 RelayHTTPURL 也要有指引, 指向工具而非 curl。
	guide := executorUsageGuide(req, true)
	if !strings.Contains(guide, execDelegateToolName) {
		t.Fatalf("mcp guide missing tool name:\n%s", guide)
	}
	if !strings.Contains(guide, "exec-9 (os: windows)") {
		t.Fatalf("mcp guide missing executor listing:\n%s", guide)
	}
	if strings.Contains(guide, "curl -sS") {
		t.Fatalf("mcp guide still teaches curl polling:\n%s", guide)
	}

	// 非 MCP 且无 RelayHTTPURL: 没有可用通道, 不给指引。
	if got := executorUsageGuide(req, false); got != "" {
		t.Fatalf("curl guide without relay url = %q, want empty", got)
	}

	// 非 MCP 但有 RelayHTTPURL: 退回 curl 指引。
	req.RelayHTTPURL = "http://relay.example"
	curlGuide := executorUsageGuide(req, false)
	if !strings.Contains(curlGuide, "curl") {
		t.Fatalf("curl fallback guide missing curl:\n%s", curlGuide)
	}

	// 无 executor peer: 两种模式都不给指引。
	req.History = nil
	if got := executorUsageGuide(req, true); got != "" {
		t.Fatalf("guide with no executors = %q, want empty", got)
	}
}
