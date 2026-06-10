package cliprovider

// mcp_delegate.go 把"agent 委派 shell 命令给 executor"从裸 curl 轮询升级为一个
// 原生 MCP 工具 exec_remote。
//
// 为什么需要它: `claude -p` 是 bridge fork 出的一次性子进程, 不订阅房间 WS, 旧做法
// 只能在 prompt 里教它 curl POST 发命令、再 sleep+curl GET 轮询 relay 找结果。延迟高、
// 易错。改造思路: bridge(持有房间 WS)注入 ProviderRequest.DelegateExec 闭包, 由它经
// 自己的 WS 发命令并阻塞等 command_result。本文件提供 agent 侧的入口——一个 in-process
// 的本地 HTTP MCP server, 暴露 exec_remote 工具, tools/call 时映射到 DelegateExec。
//
// 接入方式(与 claude_permission.go 的 --settings 注入同套路): Complete 运行期起一个
// 127.0.0.1:0 的 HTTP listener, 生成一份临时 --mcp-config JSON 指向它(带一次性 token
// header), 用 `claude -p --mcp-config <file> --strict-mcp-config --allowedTools
// mcp__agentroom__exec_remote` 挂载。--strict-mcp-config 确保只用这一个 server, 不触达
// 全局 ~/.claude.json。Complete 结束 defer close() 关 listener 并清临时文件。
//
// 传输用 Streamable HTTP(单端点 POST, 直接返回 application/json, 不开 SSE), 实现 MCP
// 最小协议子集: initialize / notifications/initialized / tools/list / tools/call。
//
// 注意这是链路 A(executor 命令, relay 经 exec-token 中介)在 agent 侧的发起入口, 与
// 链路 B(claude_permission.go 的工具授权)是相互独立的两套机制。命令最终仍走 relay
// 注入 token, 本文件既不持有也不接触 exec_token。

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"agent-room/internal/models"
)

// mcpServerName 是注入给 claude 的 MCP server 名; 工具全名即 mcp__<server>__exec_remote。
const mcpServerName = "agentroom"

// execDelegateToolName 是暴露给 agent 的工具名。
const execDelegateToolName = "exec_remote"

// mcpTokenHeader 携带一次性鉴权 token, 防同机其他进程伪造工具调用。
const mcpTokenHeader = "X-Agent-Room-MCP-Token"

// mcpProtocolVersion 是 initialize 握手回报的协议版本。
const mcpProtocolVersion = "2025-06-18"

// execDelegateBroker 在一次 Complete 期间提供"MCP exec_remote 工具 -> 房间委派"的桥接。
type execDelegateBroker struct {
	delegateExec func(ctx context.Context, r models.DelegateExecRequest) (models.DelegateExecResult, error)
	ctx          context.Context
	token        string
	endpoint     string // claude 连接的 MCP 端点 URL
	configPath   string // 临时 --mcp-config JSON 路径
	tmpDir       string
	listener     net.Listener
	server       *http.Server
}

// newExecDelegateBroker 起本地 MCP 端点并生成临时 --mcp-config。返回的 broker 的
// configPath 应作为 `claude --mcp-config` 注入; 调用方负责 defer close()。
func newExecDelegateBroker(ctx context.Context, req models.ProviderRequest) (*execDelegateBroker, error) {
	token, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("mcp delegate broker: token: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("mcp delegate broker: listen: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "agent-room-mcp-delegate-")
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("mcp delegate broker: tmp dir: %w", err)
	}

	b := &execDelegateBroker{
		delegateExec: req.DelegateExec,
		ctx:          ctx,
		token:        token,
		endpoint:     fmt.Sprintf("http://%s/mcp", listener.Addr().String()),
		tmpDir:       tmpDir,
		listener:     listener,
	}

	if err := b.writeConfig(); err != nil {
		b.close()
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", b.handleMCP)
	b.server = &http.Server{Handler: mux}
	go b.server.Serve(listener)

	return b, nil
}

// writeConfig 写一份临时 --mcp-config, 指向本地 HTTP MCP 端点并带一次性 token header。
func (b *execDelegateBroker) writeConfig() error {
	b.configPath = b.tmpDir + string(os.PathSeparator) + "mcp-config.json"
	config := map[string]any{
		"mcpServers": map[string]any{
			mcpServerName: map[string]any{
				"type": "http",
				"url":  b.endpoint,
				"headers": map[string]any{
					mcpTokenHeader: b.token,
				},
			},
		},
	}
	buf, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("mcp delegate broker: marshal config: %w", err)
	}
	if err := os.WriteFile(b.configPath, buf, 0o600); err != nil {
		return fmt.Errorf("mcp delegate broker: write config: %w", err)
	}
	return nil
}

// jsonRPCRequest / jsonRPCResponse 是我们需要的 JSON-RPC 2.0 子集。
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// handleMCP 是单端点 Streamable HTTP 处理器: 校验 token, 解析一条 JSON-RPC 消息,
// 按 method 分派。请求(有 id)直接返回 application/json; 通知(无 id)返回 202。
func (b *execDelegateBroker) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// 不提供 GET SSE 流: 我们的工具调用是请求-响应式, 无需服务端主动推送。
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 一次性 token 比对(常量时间), 防同机其他进程伪造工具调用。
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(mcpTokenHeader)), []byte(b.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}

	// 通知(无 id): initialized 等, 无需响应体。
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		writeRPCResult(w, req.ID, b.initializeResult())
	case "tools/list":
		writeRPCResult(w, req.ID, b.toolsListResult())
	case "tools/call":
		b.handleToolsCall(w, req)
	default:
		writeRPCError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// initializeResult 回报协议版本、能力(仅 tools)与 serverInfo。
func (b *execDelegateBroker) initializeResult() any {
	return map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    mcpServerName,
			"version": "1.0.0",
		},
	}
}

// toolsListResult 暴露唯一的 exec_remote 工具及其输入 schema。
func (b *execDelegateBroker) toolsListResult() any {
	return map[string]any{
		"tools": []any{
			map[string]any{
				"name":        execDelegateToolName,
				"description": "Run ONE shell command on a remote executor peer in this room and return its result (exit_code + output). Match the command syntax to the executor's OS. Use this instead of curl for delegating to executors.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"target": map[string]any{
							"type":        "string",
							"description": "The executor's bare agent id (the target_id), without any \"(os: ...)\" annotation.",
						},
						"command": map[string]any{
							"type":        "string",
							"description": "The shell command to run on the executor.",
						},
						"timeout_ms": map[string]any{
							"type":        "integer",
							"description": "Optional execution timeout in milliseconds.",
						},
						"cwd": map[string]any{
							"type":        "string",
							"description": "Optional working directory.",
						},
						"shell": map[string]any{
							"type":        "string",
							"description": "Optional shell: powershell|cmd|bash.",
						},
					},
					"required": []any{"target", "command"},
				},
			},
		},
	}
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: message},
	})
}

// toolCallParams 是 tools/call 的 params 结构。
type toolCallParams struct {
	Name      string `json:"name"`
	Arguments struct {
		Target    string `json:"target"`
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms"`
		Cwd       string `json:"cwd"`
		Shell     string `json:"shell"`
	} `json:"arguments"`
}

// handleToolsCall 解析 exec_remote 参数, 映射为一次 DelegateExec(阻塞至结果),
// 再把结果包成 MCP tool result。失败用 isError=true 的 tool result 返回, 让 agent
// 看到错误而不是协议级 error。
func (b *execDelegateBroker) handleToolsCall(w http.ResponseWriter, req jsonRPCRequest) {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if params.Name != execDelegateToolName {
		writeRPCError(w, req.ID, -32602, "unknown tool: "+params.Name)
		return
	}
	if b.delegateExec == nil {
		writeRPCResult(w, req.ID, toolErrorResult("exec delegation is not available in this session"))
		return
	}

	target := strings.TrimSpace(params.Arguments.Target)
	command := strings.TrimSpace(params.Arguments.Command)
	if target == "" || command == "" {
		writeRPCResult(w, req.ID, toolErrorResult("both target and command are required"))
		return
	}

	result, err := b.delegateExec(b.ctx, models.DelegateExecRequest{
		TargetID:  target,
		Command:   command,
		TimeoutMS: params.Arguments.TimeoutMS,
		Cwd:       strings.TrimSpace(params.Arguments.Cwd),
		Shell:     strings.TrimSpace(params.Arguments.Shell),
	})
	if err != nil {
		writeRPCResult(w, req.ID, toolErrorResult("delegation failed: "+err.Error()))
		return
	}
	writeRPCResult(w, req.ID, toolTextResult(formatDelegateResult(result), result.ExitCode != 0 || result.ErrorType != ""))
}

// formatDelegateResult 把结果渲染成给 agent 读的文本。Output 已是 executor 的
// 人读格式(exit_code + stdout/stderr), 这里仅在有额外标志时补注。
func formatDelegateResult(r models.DelegateExecResult) string {
	out := strings.TrimSpace(r.Output)
	if out == "" {
		out = fmt.Sprintf("exit_code=%d (no output)", r.ExitCode)
	}
	var notes []string
	if r.TimedOut {
		notes = append(notes, "timed_out=true")
	}
	if r.ErrorType != "" {
		notes = append(notes, "error_type="+r.ErrorType)
	}
	if len(notes) > 0 {
		out = strings.Join(notes, " ") + "\n" + out
	}
	return out
}

// toolTextResult 构造 MCP tool result(单条 text content)。
func toolTextResult(text string, isError bool) any {
	return map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
		"isError": isError,
	}
}

// toolErrorResult 是 isError=true 的便捷构造。
func toolErrorResult(text string) any {
	return toolTextResult(text, true)
}

// close 关停 MCP 端点并清理临时文件。多次调用安全。
func (b *execDelegateBroker) close() {
	if b.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = b.server.Shutdown(ctx)
		cancel()
		b.server = nil
	} else if b.listener != nil {
		b.listener.Close()
	}
	if b.tmpDir != "" {
		_ = os.RemoveAll(b.tmpDir)
		b.tmpDir = ""
	}
}
